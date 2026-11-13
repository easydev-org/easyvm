// Package walk parses a walkthrough YAML file into steps.
//
// THE FILE IS THE PROGRAM. Everything the Python implementation did in a stage script - which
// machine to start, what to type, what proves it worked - is already in these files; what this
// package does is read them without adding a second vocabulary.
package walk

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/easydev-org/easyvm/internal/step"
)

// File is a parsed walkthrough.
type File struct {
	Path string

	// Name is what the log calls this walkthrough.
	Name string
	// Resolution is the guest screen size the coordinates were measured at.
	Resolution [2]int
	// SSHPort and SSHUser are the file's own defaults; a command-line flag wins over them,
	// because a port conflict is a fact about the runner while the file describes the guest.
	SSHPort int
	SSHUser string
	// BootDiskFromCaller says this file's disk arrives as --boot-disk.
	//
	// IT EXISTS SO A FILE CAN SAY WHERE ITS DISK COMES FROM. A stage that continues from another
	// stage's output has no `disk:` step - the disk is the run's input - and without a way to say
	// so, "this file boots with no disk above it" is indistinguishable from a file that forgot to
	// create one. That distinction cost run 43044525517 three minutes: the answer CD was built and
	// the installer named before `boot:` refused.
	BootDiskFromCaller bool
	// Notes is the file's prose header, echoed into the log. It is where the measurements behind
	// the numbers in a file are written down, and a reader looking at a failed CI run needs them.
	Notes string

	// Vars are the file's own variables, resolved once against the overrides.
	Vars map[string]any

	// Steps are the steps in file order. Preamble holds the `boot:`/`record:` steps that run
	// before a pane loop; Epilogue holds the ones that run after it.
	Steps []Step
	// Panes is set for a file that matches the screen instead of executing a sequence.
	Panes []Step

	// Document is the raw mapping, for the few whole-file keys that are not steps: `waits_for`,
	// `ends`, `ends_at`, `ends_region`, `pane_limit`, `boot_timeout`, `boot_poll`, `stuck_after`.
	Document map[string]any
}

// Step is one entry of a walkthrough, with its keys already substituted.
type Step struct {
	// Index is the step's position in the file, 1-based, for logs.
	Index int
	// Name is what the log calls it.
	Name string
	// Spec is the substituted key/value mapping. Execution reads this; nothing re-reads the YAML.
	Spec map[string]any
}

// Get returns a string value, or "" when absent.
func (s Step) Get(key string) string {
	if v, ok := s.Spec[key]; ok {
		switch t := v.(type) {
		case string:
			return t
		case int:
			return strconv.Itoa(t)
		case bool:
			if t {
				return "true"
			}
			return "false"
		}
	}
	return ""
}

// Has reports whether the key is present and not false/empty.
func (s Step) Has(key string) bool {
	v, ok := s.Spec[key]
	if !ok {
		return false
	}
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return strings.TrimSpace(t) != "" && t != "false"
	case int:
		return t != 0
	}
	return true
}

// Float reads a number of seconds, accepting an int or a float and nothing else.
func (s Step) Float(key string, fallback float64) (float64, error) {
	v, ok := s.Spec[key]
	if !ok || v == nil {
		return fallback, nil
	}
	switch t := v.(type) {
	case int:
		return float64(t), nil
	case float64:
		return t, nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0, fmt.Errorf("step %q: %s must be a number, got %q", s.Name, key, t)
		}
		return f, nil
	}
	return 0, fmt.Errorf("step %q: %s must be a number", s.Name, key)
}

var placeholder = regexp.MustCompile(`\{\{\s*([^}]+?)\s*\}\}`)

// Load reads a walkthrough file and resolves its variables.
//
// THE VARIABLES ARE RESOLVED ONCE, HERE, and every step carries its resolved values. The Python
// implementation substituted per step at execution time, which meant a file could read a var that
// a later step changed - and two steps then disagreed about a value neither had written.
func Load(path string, overrides map[string]string) (*File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		// A DUPLICATE KEY IS A WORKFLOW THE RUNNER REFUSES TO LOAD, and `yaml.Unmarshal` keeps the
		// last one silently. That happened: a second `outputs:` block made GitHub reject the
		// whole workflow while every local check passed. yaml.v3 reports it, so the error is
		// passed through rather than smoothed over.
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if doc == nil {
		return nil, fmt.Errorf("%s: empty document", path)
	}

	// THE TOP-LEVEL KEYS ARE CHECKED TOO, for the same reason as the step keys: a misspelled
	// `boot_timout` would otherwise be a file that quietly used the default and waited out
	// eighteen hundred seconds with nothing to show for it.
	if err := validateDocument(doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	f := &File{Path: path, Document: doc}
	f.Name, _ = doc["name"].(string)
	f.SSHPort = 2222
	if p, ok := toInt(doc["ssh_port"]); ok && p > 0 {
		f.SSHPort = p
	}
	if v, ok := doc["boot_disk"].(string); ok && strings.TrimSpace(v) == "from_caller" {
		f.BootDiskFromCaller = true
	}
	f.SSHUser, _ = doc["ssh_user"].(string)
	if f.SSHUser == "" {
		f.SSHUser = "vmuser"
	}
	f.Resolution = [2]int{1920, 1080}
	if r, ok := doc["resolution"].([]any); ok && len(r) == 2 {
		x, okx := toInt(r[0])
		y, oky := toInt(r[1])
		if okx && oky {
			f.Resolution = [2]int{x, y}
		}
	}
	f.Notes, _ = doc["notes"].(string)

	// `work` IS ALWAYS DEFINED, because it is a fact about where the file IS rather than about the
	// run. A file that says `{{ work }}/Xcode.xip` means "beside me" when nobody named a directory,
	// and a `check` that could not resolve it would refuse a file the daemon accepts.
	if overrides == nil {
		overrides = map[string]string{}
	}
	if _, ok := overrides["args.work"]; !ok {
		if abs, err := filepath.Abs(path); err == nil {
			overrides["args.work"] = filepath.Dir(abs)
		}
	}

	f.Vars, err = resolveVars(doc["vars"], overrides)
	if err != nil {
		return nil, fmt.Errorf("%s: vars: %w", path, err)
	}

	stepsRaw, ok := doc["steps"].([]any)
	if !ok {
		return nil, fmt.Errorf("%s: no steps", path)
	}
	for i, entry := range stepsRaw {
		mapping, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: step %d is not a mapping", path, i+1)
		}
		name, _ := mapping["name"].(string)
		if name == "" {
			return nil, fmt.Errorf("%s: step %d has no name; the log would have nothing to say "+
				"when it fails", path, i+1)
		}
		// THE VOCABULARY IS CHECKED BEFORE ANYTHING RUNS, so a typo costs a parse error instead
		// of a started guest.
		if err := step.Validate(name, mapping); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		substituted, err := substitute(mapping, f.Vars)
		if err != nil {
			return nil, fmt.Errorf("%s: step %q: %w", path, name, err)
		}
		spec, _ := substituted.(map[string]any)
		f.Steps = append(f.Steps, Step{Index: i + 1, Name: name, Spec: spec})
	}
	return f, nil
}

// resolveVars turns the file's `vars:` into concrete values.
//
// A VAR MAY NAME A COMMAND-LINE ARGUMENT with `from: args.user`, and that is how a run fact gets
// into a file without the file hard-coding it. A var with neither `from` nor `value` keeps its
// `default`.
func resolveVars(raw any, overrides map[string]string) (map[string]any, error) {
	out := map[string]any{}
	decl, ok := raw.(map[string]any)
	if !ok {
		return out, nil
	}
	for name, spec := range decl {
		if m, ok := spec.(map[string]any); ok {
			if from, _ := m["from"].(string); from != "" {
				if v, ok := overrides[from]; ok && v != "" {
					out[name] = v
					continue
				}
				if v, ok := m["default"]; ok {
					out[name] = v
					continue
				}
			}
			if v, ok := m["value"]; ok {
				out[name] = v
				continue
			}
			if v, ok := m["default"]; ok {
				out[name] = v
			}
			continue
		}
		out[name] = spec
	}
	// ARGUMENTS A FILE DID NOT DECLARE ARE STILL AVAILABLE, because `args.out` and
	// `args.boot_timeout` are facts about the run and a file should not have to declare them to
	// mention them.
	for k, v := range overrides {
		if _, exists := out[strings.TrimPrefix(k, "args.")]; !exists && v != "" {
			out[strings.TrimPrefix(k, "args.")] = v
		}
		if strings.HasPrefix(k, "args.") {
			if _, exists := out[k]; !exists {
				out[k] = v
			}
		}
	}
	return out, nil
}

// Names returns the sorted var names, for an error that tells a reader what WAS defined.
func (f *File) Names() []string {
	out := make([]string, 0, len(f.Vars))
	for k := range f.Vars {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// substitute replaces `{{ name }}` throughout a value.
//
// A MISSING VAR IS AN ERROR, NOT AN EMPTY STRING. The Python implementation raised here too, and
// the reason is the same: a blank substituted into a timeout is one number in one step and a
// different number in another, which reads in a log as two components disagreeing.
func substitute(value any, vars map[string]any) (any, error) {
	switch v := value.(type) {
	case string:
		var missing string
		out := placeholder.ReplaceAllStringFunc(v, func(match string) string {
			key := strings.TrimSpace(placeholder.FindStringSubmatch(match)[1])
			got, ok := vars[key]
			if !ok {
				if missing == "" {
					missing = key
				}
				return match
			}
			return fmt.Sprintf("%v", got)
		})
		if missing != "" {
			names := make([]string, 0, len(vars))
			for k := range vars {
				names = append(names, k)
			}
			sort.Strings(names)
			return nil, fmt.Errorf("%q names %q, which is not in vars (%s)",
				v, missing, strings.Join(names, ", "))
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			sub, err := substitute(item, vars)
			if err != nil {
				return nil, err
			}
			out[i] = sub
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			sub, err := substitute(item, vars)
			if err != nil {
				return nil, err
			}
			out[k] = sub
		}
		return out, nil
	}
	return value, nil
}

func toInt(v any) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case float64:
		return int(t), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		if err == nil {
			return n, true
		}
	}
	return 0, false
}

// documentKeys is every accepted top-level key in a walkthrough file.
//
// THESE ARE NOT STEPS - they describe the whole walk - so they are listed separately rather than
// folded into the step vocabulary where they would look like things a step could say.
var documentKeys = map[string]string{
	"name":       "what the log calls this walkthrough",
	"notes":      "the file's prose header, echoed into the log; where the measurements behind its numbers live",
	"resolution": "[width, height] the coordinates were measured at",
	"ssh_port":   "the port the guest's sshd is forwarded to",
	"ssh_user":   "the account the walk logs in as",
	"vars":       "the file's own variables, some of which name command-line arguments",
	"machine": "HOW THIS GUEST IS BUILT AND STARTED, separately from what is done to it. " +
		"A file may say `machine: {disk_gb: 128, fetch: {...}, boot: {...}}` and then keep " +
		"`steps:` for the panes, typing and waiting - so a sandbox can start the machine " +
		"without running the program against it. A file without this block is unchanged: its " +
		"leading `disk:`/`fetch:`/`boot:` steps ARE its machine",
	"steps":             "the steps, in file order",
	"waits_for":         "a pane file's arrival gate: the words that mean the first pane is up",
	"boot_timeout":      "how long a guest may take to reach its desktop",
	"boot_poll":         "seconds between looks while waiting for it",
	"ends":              "a pane file's end condition, read whole",
	"ends_at":           "a pane file's end condition read from a crop, for a control too small to read whole",
	"ends_region":       "the crop the end condition is read in",
	"ends_region_scale": "enlargement for that crop",
	"pane_limit":        "how many times one pane may be answered before it is reported rather than clicked",
	"stuck_after":       "seconds of nothing matching before a pane walk reports STUCK",
	"boot_disk": "the disk comes from the caller's --boot-disk rather than from a `disk:` step. " +
		"A file that says this is one whose disk was made by an earlier stage - every macOS " +
		"stage after setup, and the Windows stages after the bake",
}

func validateDocument(doc map[string]any) error {
	for key := range doc {
		if _, ok := documentKeys[key]; ok {
			continue
		}
		names := make([]string, 0, len(documentKeys))
		for k := range documentKeys {
			names = append(names, k)
		}
		sort.Strings(names)
		return fmt.Errorf("unknown top-level key %q; a file may say %s",
			key, strings.Join(names, ", "))
	}
	return nil
}
