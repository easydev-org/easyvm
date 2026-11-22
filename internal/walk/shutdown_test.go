package walk

import (
	"path/filepath"
	"strings"
	"testing"
)

// A POWEROFF SENT WITHOUT `sudo` IS A REQUEST THAT IS REFUSED, AND THE REFUSAL IS SILENT.
//
// `shutdown: ssh` used to send `poweroff` as the ssh user, who is a sudoer but not root. On every
// distribution here that exits 1 - and the OLD code only logged a CONNECTION error, so a status of 1
// looked like success. The machine stayed up, `Stop` waited the whole grace, and the stage ended
// with:
//
//	the guest did not power off within 5m0s; killing it - the disk will need a journal replay
//
// So every `shutdown: ssh` in this tree was a 300-second delay followed by a kill. The files now say
// `ssh: printf '%s\n' '{{ password }}' | sudo -S -p ” poweroff` then `shutdown: wait`, and this test
// is what stops the `sudo` being dropped again on a later edit.
//
// `sudo -v` WOULD NOT DO: its timestamp lasts five minutes and an install is forty, so the unlock
// from an earlier step has expired by the time the shutdown runs.
func TestEveryPoweroffCarriesItsPassword(t *testing.T) {
	root := filepath.Join("..", "..")
	files := globStepsAny(root)
	if len(files) == 0 {
		t.Skip("no walkthroughs here")
	}

	checked := 0
	for _, path := range files {
		f, err := Load(path, nil)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(root, path)
		var walk func([]Step)
		walk = func(steps []Step) {
			for _, s := range steps {
				cmd := s.Get("ssh")
				if !mentionsPoweroff(cmd) {
					goto children
				}
				checked++
				if !strings.Contains(cmd, "sudo") {
					t.Errorf("%s: step %q sends poweroff WITHOUT sudo. The ssh user is a sudoer, "+
						"not root, so the request exits 1 and the guest never stops - and `shutdown: "+
						"wait` then waits out its grace for a machine nobody asked to stop",
						rel, s.Name)
				}
				// `sudo -S` READS THE PASSWORD FROM STDIN, so something has to feed it. The file
				// wrote `{{ password }}`, which Load has already substituted - what survives is the
				// `printf ... |` that carries it, so that is what this checks.
				if !strings.Contains(cmd, "sudo -S") && !strings.Contains(cmd, "sudo -n") {
					t.Errorf("%s: step %q sends poweroff with sudo but not `sudo -S` (read the "+
						"password from stdin) and not `sudo -n` (assume it is cached). A plain "+
						"`sudo poweroff` over a non-tty session waits for a password nobody types",
						rel, s.Name)
				}
				if strings.Contains(cmd, "sudo -S") && !strings.Contains(cmd, "|") {
					t.Errorf("%s: step %q uses `sudo -S` with nothing piped into it, so sudo reads "+
						"an empty password and refuses", rel, s.Name)
				}
			children:
				if lp, ok := s.Spec["repeat"].(map[string]any); ok {
					if inner, ok := lp["do"].([]any); ok {
						walk(stepsOf(inner))
					}
				}
			}
		}
		walk(f.Steps)
	}
	t.Logf("checked %d poweroff step(s)", checked)
}

// mentionsPoweroff reports whether a shell line actually runs poweroff.
//
// IT LOOKS FOR THE COMMAND AT A WORD BOUNDARY rather than for the substring, because a step that
// says "the poweroff did not happen" in an echo is not sending one.
func mentionsPoweroff(cmd string) bool {
	if cmd == "" {
		return false
	}
	for _, field := range strings.FieldsFunc(cmd, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\t' || r == ';' || r == '|' || r == '&'
	}) {
		if field == "poweroff" || field == "shutdown" {
			return true
		}
	}
	return false
}

// A POWEROFF STEP MUST BE FOLLOWED BY SOMETHING THAT WAITS FOR THE GUEST TO GO.
//
// `ssh: poweroff` DELIVERS THE REQUEST AND RETURNS; it does not wait for qemu to exit. The next step
// that touches the disk would then read a file qemu still holds open, which is the failure the
// `shutdown: wait` half exists to prevent.
func TestEveryPoweroffIsFollowedByAWait(t *testing.T) {
	root := filepath.Join("..", "..")
	files := globStepsAny(root)
	if len(files) == 0 {
		t.Skip("no walkthroughs here")
	}
	for _, path := range files {
		f, err := Load(path, nil)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(root, path)
		for i, s := range f.Steps {
			// A REQUEST THAT STOPS THE GUEST NEEDS A WAIT AFTER IT, WHATEVER SENT IT: the power
			// button, the agent, or a command over ssh. `wait_until: stopped` is that wait, and it is
			// a step of its own because `shutdown:` names how to STOP the machine - a method that
			// sent nothing was a verb that did not happen.
			asked := s.Get("acpi") == "power" || s.Get("qga") == "shutdown" ||
				mentionsPoweroff(s.Get("ssh"))
			if !asked {
				continue
			}
			found := false
			for _, later := range f.Steps[i+1:] {
				if later.Get("wait_until") == "stopped" {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s: step %q asks the guest to stop and no later step says "+
					"`wait_until: stopped`. The next step to read the disk would find qemu still "+
					"holding it open", rel, s.Name)
			}
		}
	}
}
