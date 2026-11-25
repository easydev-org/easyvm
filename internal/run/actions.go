package run

import (
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/easydev-org/easyvm/internal/bootstrap"
	"github.com/easydev-org/easyvm/internal/screen"
	"github.com/easydev-org/easyvm/internal/walk"
)

// --- the machine ----------------------------------------------------------------------

func (e *Executor) doBoot(s walk.Step) error {
	m := e.Driver.Machine()
	if m == nil {
		return fail(s.Name, "this driver has no machine to start")
	}

	// A MACHINE THAT IS ALREADY RUNNING IS NOT AN ERROR, BECAUSE A YAML IS AN ACTION ON A MACHINE.
	//
	// THE FIRST MODEL WAS ONE FILE PER MACHINE: every walkthrough booted, did its steps and closed.
	// A SANDBOX IS THE OTHER WAY ROUND - `init.yaml` installs, `dev.yaml` adds a toolchain, `test.yaml`
	// runs the tests, ALL ON ONE MACHINE THAT WAS BOOTED ONCE.
	//
	// So a `boot:` step whose machine is already up is a NO-OP WITH A LOG LINE rather than a failure.
	// It is the same rule as a `mkdir -p`: the step says "this machine must be running", and it is.
	//
	// THE `os:` STILL HAS TO MATCH, when the step names one. A file that says `os: windows` against a
	// running Linux guest is a mistake, not a no-op, and it would otherwise be discovered by a click
	// landing on the wrong screen.
	// A RUNNING MACHINE IS REUSED RATHER THAN COMPARED, because a boot no longer names a guest - it
	// names qemu arguments. Two boots in one file that write the SAME arguments are one machine, which
	// is what a sandbox means; two that write different ones are a different machine, and the only
	// honest answer to that is a `stop:` first, because a daemon holds one qemu.
	//
	// THE ARGUMENTS ARE ALREADY RUNNING, so a differing set is refused rather than silently ignored:
	// a file that asked for a different machine and got the old one would discover it as a screen that
	// never changes.
	if running, ok := m.(interface{ IsRunning() bool }); ok && running.IsRunning() {
		if same, known := m.(interface{ RunningTheSame([]string) bool }); known && !same.RunningTheSame(bootArgs(s)) {
			return fail(s.Name, "this machine is already running with different qemu arguments. "+
				"One daemon holds one machine, so a boot that describes another one needs a `stop:` "+
				"first - otherwise the steps below drive the guest the earlier boot started")
		}
		e.Driver.Log(fmt.Sprintf("  already running: %s", s.Name))
		return nil
	}
	// `family:` WAS THE SAME KEY UNDER ANOTHER NAME and is gone: MEASURED 15 uses against `os:`'s
	// 56, every one of them meaning the same thing, and the reader had to guess which spelling a
	// given file used. `os:` is the name.
	// THERE IS NO `os:` - a boot names qemu arguments, and the engine does not classify the guest.

	// `boot_from:` IS A RESUME, AND IT IS A SEPARATE KEY FROM `boot:` ON PURPOSE.
	//
	// A step that says `boot: true` asks for a boot loader to run; one that says `boot_from:` asks for
	// a machine whose memory is already decided. They produce the same running guest by different
	// means, and a file that meant one and got the other would take minutes to notice - the guest
	// boots happily from the wrong state and the failure appears as "my data is missing".
	resumeFrom := s.Get("boot_from")
	if resumeFrom != "" && !filepath.IsAbs(resumeFrom) {
		resumeFrom = filepath.Join(e.Work, resumeFrom)
	}
	if resumeFrom != "" {
		if _, err := os.Stat(resumeFrom); err != nil {
			return fail(s.Name, "no snapshot at %s: %v. A `boot_from:` names a file an earlier "+
				"`snapshot:` wrote", resumeFrom, err)
		}
	}
	spec := BootSpec{
		Disk:     e.bootDisk(),
		// THE FILE'S OWN ARGUMENTS, WHICH ARE THE WHOLE OF WHAT IT SAYS ABOUT ITS MACHINE.
		//
		// THERE IS NO `os:`, NO `iso:` AND NO `microvm:`: a boot names qemu arguments. The engine
		// contributes only what the host owns - the machine string it was given, `-display none`, the
		// serial file a step polls, and this daemon's own qmp and vnc sockets.
		Machine:  s.Get("machine"),
		Args:     qemuArgPairs(s),
		Dir:      s.Get("dir"),
		Incoming: resumeFrom,
		// THE DISK IS FRESH IF THIS FILE CREATED IT. `disk_gb:` is the only step that makes a disk
		// rather than naming one, so it is the only thing that can answer this - and the machine needs
		// the answer twice: a fresh disk runs on itself (no throwaway diff that `ReleaseDiff` deletes),
		// and a disk that came from somewhere keeps its writes.
		FreshDisk: e.madeDisk,
		// AND THE FILE MAY ASK FOR THE SAME THING EXPLICITLY, with `overlay: false`. That is for a
		// stage whose diff does not fit beside the disk it starts from - see
		// `machine.Boot.WantsOverlay`. `Bool` is what reads it, because the default is ON: an absent
		// key and `overlay: true` both mean "a diff", and only an explicit `false` turns it off.
		NoOverlay: noOverlayFor(s),
	}
	e.Driver.Log(fmt.Sprintf("  booting with %d qemu argument(s)", len(spec.Args)))
	if err := m.Boot(spec); err != nil {
		return fail(s.Name, "boot: %v", err)
	}
	// THE BUDGET THE FILE PUT ON THE BOOT IS HANDED TO THE WAIT THAT FOLLOWS IT.
	//
	// `doBoot` starts qemu and returns, and every waiting step after it had a hard-coded default -
	// `untilConsole` 180 seconds, `doRequireSSH` 900. A file that wrote `timeout == 900` on its boot
	// was describing its guest to a step that never read it. See Executor.bootBudget.
	if budget := seconds(s, "timeout", 0); budget > 0 {
		e.bootBudget = budget
		e.bootBudgetSet = true
		e.Driver.Log(fmt.Sprintf("  the boot's %s becomes the budget for the wait that follows",
			budget))
	}
	e.Driver.Log("  qemu started")
	return nil
}

// bootSourceName is what the log calls this: the fact a reader checks first.
func bootSourceName(spec BootSpec) string {
	switch {
	case spec.Recovery != "":
		return "the recovery image"
	case spec.ISO != "":
		return "the installer"
	}
	return "the disk"
}

// mediaNote lists the media, because a boot line that names only the primary medium hides the other
// two a Windows install needs.
func mediaNote(spec BootSpec) string {
	var parts []string
	for _, m := range []struct{ path, why string }{
		{spec.ISO, "installer"},
		{spec.AnswerCD, "answer"},
		{spec.DriverCD, "drivers"},
		{spec.Recovery, "recovery"},
	} {
		if m.path != "" {
			parts = append(parts, m.why+"="+filepath.Base(m.path))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " + " + strings.Join(parts, " + ")
}

// assetPath resolves a medium a `fetch:` produced.
//
// `answer_cd: answer_iso` MEANS "THE FILE THE answer_iso FETCH WROTE", not a path. That keeps the
// name of a produced file in one place - the asset layer - instead of in a YAML file that would
// then disagree with it. Anything else is used as a path, so a file run by hand against a medium
// it prepared itself still works.
func (e *Executor) assetPath(v string) string {
	if v == "" {
		return ""
	}
	if p, ok := e.assets[v]; ok {
		return p
	}
	// A NAME THAT IS NOT AN ASSET ROLE IS A PATH, AND A RELATIVE ONE IS RELATIVE TO THE WORK
	// DIRECTORY - NOT TO THE PROCESS'S CURRENT DIRECTORY.
	//
	// MEASURED on run 43879668884, whose install leg had the ISO right there:
	//
	//	=== the media this stage boots from is already on disk
	//	  required: freebsd.iso
	//	error: boot the installer: boot: no the installer medium at freebsd.iso:
	//	       stat freebsd.iso: no such file or directory
	//
	// `require:` joins `e.Work` and finds the file; `boot:` passed the bare name to qemu, which
	// resolved it against the PROCESS'S directory - `/__w/easyvm/easyvm`, where no ISO lives. Two
	// steps, two answers about one file, and the one that failed named the file rather than the
	// directory it looked in.
	//
	// SO EVERY RELATIVE PATH IS JOINED HERE, once, for every role a boot step can name.
	if !filepath.IsAbs(v) {
		return filepath.Join(e.Work, v)
	}
	return v
}

// bootDisk is the target, from the `disk:` step when it ran and from the flag otherwise.
func (e *Executor) bootDisk() string {
	if d, ok := e.Driver.Machine().(interface{ Disk() string }); ok {
		if p := d.Disk(); p != "" {
			return p
		}
	}
	return e.BootDisk
}

func (e *Executor) doShutdown(s walk.Step) error {
	m := e.Driver.Machine()
	if m == nil {
		return fail(s.Name, "this driver has no machine to stop")
	}
	method := s.Get("shutdown")
	if method == "" {
		return fail(s.Name, "`shutdown:` must name a method: guest_agent, ssh, acpi or kill")
	}
	e.Driver.Log(fmt.Sprintf("  stopping with method %q", method))
	// THE STEP'S OWN `timeout:` IS THE GRACE THE GUEST GETS, and passing it is the whole point: a
	// stage that just wrote a desktop needs longer than the five-minute default, and a kill after a
	// fixed five minutes leaves a filesystem that needs a replay.
	//
	// `seconds` ALREADY RETURNS A `time.Duration`, so multiplying by `time.Second` again squared the
	// unit. MEASURED with the arithmetic this line used to do:
	//
	//	timeout: 300  ->  1347804h7m  =  153.9 YEARS
	//
	// and `waitForExit` was given that as its deadline, so a `shutdown: ssh` step waited for a guest
	// that had already been asked to power off and would never exhaust the grace and reach the kill.
	// TWO SUCH STAGES SAT IN "stop the machine" FOR FORTY MINUTES AND WERE STILL RUNNING when this was
	// found - macos-ssh run 43944852857 and windows-vm-configure run 43962063140 - and neither could
	// ever report. A `shutdown: acpi` stage looked healthy only because qemu exits on its own.
	//
	// THE ZERO FALLBACK STILL MEANS "THE DRIVER'S DEFAULT": `graceOrDefault` reads `> 0`.
	grace := seconds(s, "timeout", 0)
	if err := m.Stop(method, grace); err != nil {
		return fail(s.Name, "%v", err)
	}
	// AND THE DISK IS GIVEN A MOMENT TO SETTLE, BECAUSE A STOPPED PROCESS IS NOT A FLUSHED FILE.
	//
	// qemu is gone and the file is still moving: the kernel writes back pages it has not committed,
	// and a qcow2 whose metadata is half on disk is a disk `qemu-img convert` refuses. THE WAIT IS
	// HERE RATHER THAN A STEP because every stage needs it and none of them has an opinion about how
	// long it takes - a `disk_settle 300` step in a walkthrough used to ask, which put a duration in
	// a place that could not know one.
	//
	// IT IS BOUNDED AND SHORT. A disk that has not stopped growing in ten seconds after its writer
	// died is not settling; it is being written by something else, and the step should fail rather
	// than block a leg for an hour.
	if disk := e.bootDisk(); disk != "" {
		if err := waitForFileToSettle(disk, 10*time.Second); err != nil {
			e.Driver.Log("  " + err.Error())
		}
	}
	return nil
}

// waitForFileToSettle blocks until a file's size stops changing, or the window expires.
//
// IT IS NOT A CORRECTNESS CHECK, it is a courtesy: a caller that reads the file immediately after this
// returns will see what the writer left. A file that keeps growing is REPORTED and not fatal, because
// the step that called this has done its work - `save:` will fail with its own message if the disk is
// really unusable.
func waitForFileToSettle(path string, window time.Duration) error {
	deadline := time.Now().Add(window)
	var last int64 = -1
	var quietFrom time.Time
	for time.Now().Before(deadline) {
		fi, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if fi.Size() != last {
			last, quietFrom = fi.Size(), time.Time{}
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if quietFrom.IsZero() {
			quietFrom = time.Now()
		} else if time.Since(quietFrom) >= time.Second {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("%s is still growing after %s; something other than the guest is writing it",
		filepath.Base(path), window)
}

// translateDisk turns the mapping form of `disk:` into the step that carries the old keys.
//
// IT IS A PURE FUNCTION SO A TEST CAN ASSERT THE TRANSLATION without a guest. It returns nil when
// the value is not a mapping, or when a mapping has no `file:`.
func translateDisk(spec map[string]any) map[string]any {
	m, ok := spec["disk"].(map[string]any)
	if !ok {
		return nil
	}
	file, _ := m["file"].(string)
	if file == "" {
		return nil
	}
	out := make(map[string]any, len(spec)+6)
	for k, v := range spec {
		if k != "disk" {
			out[k] = v
		}
	}
	out["disk"] = file
	for from, to := range map[string]string{
		"size": "disk_gb", "gpt": "disk_gpt", "esp_mib": "disk_esp_mib",
		"min_mb": "disk_min_mb", "wait": "disk_settle",
	} {
		if v, ok := m[from]; ok {
			out[to] = v
		}
	}
	return out
}

func (e *Executor) doDisk(s walk.Step) error {
	// A STEP WITH `disk_gb:` CREATES A DISK; one with `disk:` NAMES AN EXISTING ONE.
	//
	// THEY ARE DIFFERENT ACTIONS AND THE PRESENCE OF THE KEY SAYS WHICH. A `disk:` step prepares a
	// copy of a disk that came from somewhere; `disk_gb:` makes the empty 128 GB disk an INSTALL
	// writes to, which is the first thing stage one needs and nothing else does.
	if s.Has("disk_gb") {
		return e.createDisk(s)
	}
	v, ok := s.Spec["disk"]
	if !ok {
		return fail(s.Name, "`disk:` must name a file")
	}
	// `disk:` AS A MAPPING IS THE FOLDED FORM OF FIVE KEYS: `disk_gb`, `disk_gpt`, `disk_esp_mib`,
	// `disk_min_mb` and `disk_settle` all described the disk the line already named. Measured before
	// folding: `disk:` 30 uses, `disk_gb:` 17, `disk_min_mb:` 7, `disk_settle:` 7, `disk_esp_mib:` 1,
	// `disk_gpt:` 1 - six keys for one file's size and layout.
	//
	//	disk: disk.qcow2                                     an existing disk
	//	disk: {file: disk.qcow2, size: 128}                  create it, 128 GiB
	//	disk: {file: disk.qcow2, size: 128, gpt: true}       ...with a GPT table
	//	disk: {file: disk.qcow2, min_mb: 4000, settle: 300}  wait for it to stop growing
	//
	// The mapping's fields are translated into the old keys, so `createDisk` - and every guard in
	// it - is reused rather than duplicated.
	if _, isMap := v.(map[string]any); isMap {
		spec := translateDisk(s.Spec)
		if spec == nil {
			return fail(s.Name, "`disk:` as a mapping needs `file:` - "+
				"`disk: {file: disk.qcow2}` or `disk: {file: disk.qcow2, size: 128}`")
		}
		sub := walk.Step{Index: s.Index, Name: s.Name, Spec: spec}
		if _, creates := spec["disk_gb"]; creates {
			return e.createDisk(sub)
		}
		return e.doDisk(sub)
	}
	path, isPath := v.(string)
	if !isPath {
		return fail(s.Name, "`disk:` must be a file name or a mapping, got %T", v)
	}
	if path == "" {
		return fail(s.Name, "`disk:` is empty")
	}
	// A RELATIVE PATH IS LOOKED FOR IN BOTH PLACES, and the work directory wins.
	//
	// A file says `disk: disk.qcow2`, meaning "the prepared copy I am about to run on". In a CI job
	// that copy is in the work directory the stage created; when the file is run by hand it is
	// often beside the YAML. Trying `work` first and falling back to the file's own directory
	// serves both without the file having to know which one it is in.
	if !filepath.IsAbs(path) {
		inWork := filepath.Join(e.Work, path)
		beside := filepath.Join(filepath.Dir(e.File.Path), path)
		switch {
		case fileExists(inWork):
			path = inWork
		case fileExists(beside):
			path = beside
		default:
			return fail(s.Name, "no disk at %s or %s (work=%q, file=%q); a `disk:` step names "+
				"the file `boot:` will start, and this stage was given neither",
				inWork, beside, e.Work, e.File.Path)
		}
	}
	e.Driver.Log(fmt.Sprintf("  disk: %s", path))
	if _, err := os.Stat(path); err != nil {
		return fail(s.Name, "no disk at %s: %v", path, err)
	}
	if setter, ok := e.Driver.Machine().(interface{ SetDisk(string) }); ok {
		setter.SetDisk(path)
	}
	return nil
}

// createDisk makes an empty qcow2 for an install to write to.
//
// THE SIZE IS FIXED AT 128G BY EVERY FILE THAT USES THIS, and it is not a tunable: macOS's setup
// assistant picks its target by CAPACITY, and every coordinate in macos/15/steps was measured on the
// disk the workflow actually bakes. A different size moves a window that was measured once.
func (e *Executor) createDisk(s walk.Step) error {
	// A COUNT, NOT A DURATION. `seconds()` RETURNS A time.Duration, and reading `disk_gb` through
	// it produced a size of 128 * 10^9 - which qemu-img refused with
	//
	//     Invalid image size specified. Must be between 0 and 9223372036854775807.
	//
	// a message about the NUMBER that names neither the key nor the unit. `s.Float` reads the number
	// the file wrote.
	gbFloat, err := s.Float("disk_gb", 128)
	if err != nil || gbFloat <= 0 {
		return fail(s.Name, "`disk_gb:` must be a positive number of gigabytes, got %v",
			s.Spec["disk_gb"])
	}
	gb := int(gbFloat)
	path := s.Get("disk")
	if path == "" {
		return fail(s.Name, "a `disk_gb:` step must also name the file it creates: `disk: disk.qcow2`")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(e.Work, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fail(s.Name, "%v", err)
	}
	// A PREVIOUS FILE IS REPLACED, NOT REUSED. Starting from whatever a failed run left is how an
	// install writes into half a system and reports success - and the step's whole job is "a disk
	// that does not exist yet".
	if fileExists(path) {
		e.Driver.Log(fmt.Sprintf("  removing the disk a previous run left at %s", path))
		if err := os.Remove(path); err != nil {
			return fail(s.Name, "%v", err)
		}
	}

	qemuImg, err := exec.LookPath("qemu-img")
	if err != nil {
		return fail(s.Name, "qemu-img is not installed: %v", err)
	}
	e.Driver.Log(fmt.Sprintf("  creating %s, %d GiB", path, gb))
	// `-f qcow2` AND NO SPARSE SURPRISE: a qcow2 grows as it is written, so a 128G file is a few
	// hundred KiB until an installer puts something in it.
	cmd := exec.Command(qemuImg, "create", "-f", "qcow2", path, fmt.Sprintf("%dG", gb))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fail(s.Name, "qemu-img create: %v (%s)", err, firstLineOf(string(out)))
	}
	fi, err := os.Stat(path)
	if err != nil {
		return fail(s.Name, "qemu-img reported success and %s is not there", path)
	}
	e.Driver.Log(fmt.Sprintf("  %s: %d KiB (a qcow2 grows as it is written)", filepath.Base(path),
		fi.Size()>>10))

	// A GPT IS WRITTEN HERE RATHER THAN BY THE INSTALLER, AND THAT IS A MEASURED REQUIREMENT.
	//
	// Android-x86's first pane lists the partitions IT FINDS. Given a blank disk it offers
	// "Create/Modify partitions" and the walkthrough has to drive cgdisk - seventeen line prompts in
	// a TUI that accepts `260M`, `EF00` and a name as TYPED LINES, where one misaligned key silently
	// changes the ESP's type to "Linux filesystem" and the install then has no ESP at all.
	//
	// Given the table below it shows `vda1 0.25GB` already selected, and ten key presses finish the
	// install. The ESP must also be 1 MiB aligned and typed EF00 - measured, twice: a table whose
	// first partition starts at LBA 34 makes the installer report "No ESP is selected", and one
	// whose ESP type GUID is not C12A7328-F81F-11D2-BA4B-00A0C93EC93B makes it answer "This is not
	// an EFI System Partition, please check again".
	if s.Has("disk_gpt") {
		espMiB, _ := s.Float("disk_esp_mib", 260)
		if err := writeGPT(path, gb, int(espMiB)); err != nil {
			return fail(s.Name, "%v", err)
		}
		e.Driver.Log(fmt.Sprintf("  GPT: ESP %d MiB at LBA 2048 (type EF00), the rest Linux",
			int(espMiB)))
	}

	if setter, ok := e.Driver.Machine().(interface{ SetDisk(string) }); ok {
		setter.SetDisk(path)
	}
	// THE DISK IS THIS RUN'S, AND A LATER `boot:` MUST KNOW IT. This is the only step that
	// creates a disk; every other way of naming one points at a file that already existed, whose
	// writes must survive. See machine.Boot.FreshDisk for what the conflation cost.
	e.madeDisk = true
	return nil
}

// --- host commands --------------------------------------------------------------------

func (e *Executor) doSCP(s walk.Step) error {
	client := e.Driver.Guest()
	if client == nil {
		return fail(s.Name, "no ssh connection; a `require_ssh:` step must come first")
	}
	// `scp: {template: launcher, to: /tmp/x}` IS THE ONE-STEP FORM, and it replaces a `write:` step
	// plus the `scp:` that copied what it wrote. Measured: all 16 `write:` steps were followed
	// immediately by exactly that `scp:`.
	if m, ok := s.Spec["scp"].(map[string]any); ok {
		kind, _ := m["template"].(string)
		remote, _ := m["to"].(string)
		if kind == "" || remote == "" {
			return fail(s.Name, "`scp:` as a mapping needs `template:` (what to render) and "+
				"`to:` (where it goes): `scp: {template: launcher, to: /tmp/bootstrap-launcher.sh}`")
		}
		local, err := e.writeTemplate(s, kind)
		if err != nil {
			return err
		}
		path := filepath.Join(e.Work, local)
		if err := scpFile(client, path, remote); err != nil {
			return fail(s.Name, "scp %s -> %s: %v", local, remote, err)
		}
		e.Driver.Log(fmt.Sprintf("  scp: %s (%s) -> %s", local, kind, remote))
		return nil
	}
	raw, ok := s.Spec["scp"].([]any)
	if !ok || len(raw) != 2 {
		return fail(s.Name, "`scp:` must be [local-file, remote-path]")
	}
	local, _ := raw[0].(string)
	remote, _ := raw[1].(string)
	if local == "" || remote == "" {
		return fail(s.Name, "`scp:` must name both ends")
	}
	// A RELATIVE LOCAL PATH IS LOOKED FOR WHERE `write:` PUT IT, WHICH IS THE WORK DIRECTORY.
	//
	// Measured on manjaro's vm leg:
	//
	//	copy the launcher in: scp bootstrap-launcher.sh -> /tmp/bootstrap-launcher.sh:
	//	open bootstrap-launcher.sh: no such file or directory
	//
	// `write: launcher` WROTE IT and `scp:` could not find it, because the writer joins its name onto
	// the work directory and the reader used the process's own working directory. The two are
	// different places and the file was in the first one.
	//
	// THE WORK DIRECTORY WINS, and the caller's directory is the fallback - the same rule `disk:`
	// already follows, for the same reason: a file run by hand has its inputs beside it and a file run
	// by the daemon has them in `work`.
	if !filepath.IsAbs(local) {
		inWork := filepath.Join(e.Work, local)
		if fileExists(inWork) {
			local = inWork
		}
	}
	// A MISSING FILE NAMES EVERY PLACE IT WAS LOOKED FOR. The message above said only
	// `open bootstrap-launcher.sh: no such file or directory`, which reads as "the copy failed"
	// rather than "the step that should have produced it did not, or produced it elsewhere".
	if _, statErr := os.Stat(local); statErr != nil {
		return fail(s.Name, "no file to copy at %s (work=%q): %v. A `scp:` names a file that an "+
			"earlier `write:` or `fetch:` produced; if that step ran, look at where IT wrote",
			local, e.Work, statErr)
	}
	// A TEXT FILE A STEP CARRIES IS RENDERED LIKE A STEP'S OWN KEYS. The loader substitutes
	// `{{ name }}` inside the walk file and nowhere else, so a helper script copied verbatim kept its
	// placeholders. MEASURED ON RUN 44900459640:
	//
	//	/tmp/run-xcode-install.sh: line 30: user: command not found
	//
	// for `WORK=/Users/{{ user }}/xcode-expand`. The rendered copy goes to a temporary file beside the
	// original and is deleted after the transfer, so the repository keeps its placeholders and the
	// guest receives values.
	local = e.renderForCopy(s, local)
	if err := scpFile(client, local, remote); err != nil {
		return fail(s.Name, "scp %s -> %s: %v", filepath.Base(local), remote, err)
	}
	e.Driver.Log(fmt.Sprintf("  scp: %s -> %s", filepath.Base(local), remote))
	return nil
}

// noOverlayFor answers "does this boot skip the throwaway diff?", and only an explicit
// `overlay: false` says yes.
//
// IT IS A TOP-LEVEL KEY BY THE TIME THE ENGINE SEES IT. `boot` in a 2.0 file is a MARKER that the
// reader flattens: its children are lifted to sit beside it (`v2NormaliseList` in walkv2.go does the
// move), so `machine`, `args` and `overlay` all arrive in the step's own map. A reader that looked
// inside a nested `boot:` map would find nothing at all.
//
// THE DEFAULT IS A DIFF. Every stage that boots a published disk reads through a read-only base, and
// that is what makes a failed run harmless and a guest's TRIM safe. A file opts out by name.
func noOverlayFor(s walk.Step) bool {
	if on, present := s.Bool("overlay"); present {
		return !on
	}
	return false
}

// --- the share ------------------------------------------------------------------------// --- recording and publishing ---------------------------------------------------------

func (e *Executor) doCompress(s walk.Step) error {
	dest := s.Get("compress")
	if dest == "" {
		return fail(s.Name, "`compress:` must name the artifact path")
	}
	source := e.Driver.Machine()
	if source == nil {
		return fail(s.Name, "no machine is known; a `boot:` step must come first")
	}
	// THE DISK IS READ WITH qemu-img, SO QEMU MUST NOT STILL HAVE IT OPEN - and the step that waits
	// for that is `wait_until: stopped`, which a file writes where it wants it. Doing the wait here
	// as well would make "did anyone stop the machine?" a question with two answers.
	disk := ""
	if d, ok := source.(interface{ Disk() string }); ok {
		disk = d.Disk()
	}
	if disk == "" {
		return fail(s.Name, "no disk is known; a `disk:` step must come first")
	}

	// AN EMPTY DISK IS NOT A PRODUCT, AND PUBLISHING ONE IS THE WORST KIND OF SUCCESS.
	//
	// MEASURED ON ghostbsd: the walk ran all 25 steps, reported `finished`, and left
	//
	//	/disks/os/ghostbsd/install.qcow2   198656 bytes
	//
	// - the size `qemu-img create` writes for a disk nobody has touched. Its `installation runs` step
	// is `wait: 1800`, A FIXED THIRTY MINUTES WITH NO CHECK THAT ANYTHING IS HAPPENING, so a guest whose
	// installer never started still "completes".
	//
	// THE FLOOR IS THE CHECK THAT CANNOT BE FAKED. 512 MiB is well above a fresh qcow2's metadata and
	// well below any real install - the smallest system here is ~1.2 GiB - so it separates "an
	// installer wrote a system" from "nothing wrote anything" with no judgement in between.
	//
	// IT IS `disk_min_mb:` WHEN A FILE SAYS SO, because a file that produces a deliberately small
	// artifact should be able to say what small means for it.
	floor, _ := s.Float("disk_min_mb", 512)
	if size, how := installedBytes(disk); size >= 0 && floor > 0 {
		if mb := float64(size) / (1 << 20); mb < floor {
			return fail(s.Name, "the disk is %.0f MiB, below the %.0f MiB floor for an installed "+
				"system (%s). This is a qcow2 header with nothing written to it - the steps above "+
				"ran without installing anything, and publishing it would put an empty disk behind "+
				"a tag the next stage pulls", mb, floor, how)
		}
	}

	// `qemu-img convert` READS THE WHOLE BACKING CHAIN AND WRITES ONE FILE, so it is the flatten.
	// There is no second call for an overlay: what this step publishes is either the disk the run
	// wrote (an install) or the diff on top of the disk it was given, and convert reads both.
	if err := compressDisk(disk, dest, e.Driver.Log); err != nil {
		return fail(s.Name, "%v", err)
	}

	// AND THE DIFF IS RELEASED AFTER IT HAS BEEN READ. A run that stops here and never releases
	// leaves a file whose base has never been re-checked, and the check is the guarantee.
	if rel, ok := source.(interface{ ReleaseDiff() error }); ok {
		if err := rel.ReleaseDiff(); err != nil {
			return fail(s.Name, "%v", err)
		}
	}
	return nil
}

// notImplementedCount reports how many steps a file has that this build cannot run, so `run` can
// refuse before starting a machine rather than after.
func notImplementedCount(steps []walk.Step, can map[string]bool) (int, string) {
	count := 0
	first := ""
	passive := map[string]bool{"name": true, "optional": true, "timeout": true,
		"wait_after": true, "poll": true, "os": true, "family": true}
	for _, s := range steps {
		blocking := false
		for k := range s.Spec {
			if passive[k] || can[k] {
				continue
			}
			blocking = true
			break
		}
		if blocking {
			count++
			if first == "" {
				first = s.Name
			}
		}
	}
	return count, first
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// --- host-side files ------------------------------------------------------------------

// doWrite writes one of the bootstrap files on the host, for the `scp:` after it.
//
// IT DOES NOT COPY AND DOES NOT RUN. That split is from the Python implementation and it was
// earned: a single step that wrote, copied and ran reported `the launcher install exited 1` and
// named neither the file nor the action, so a failure could only be diagnosed by reading the script.
//
// `family:` IS TAKEN FROM THE STEP WHEN IT NAMES ONE, and from the walkthrough otherwise - the same
// rule for the same reason as everywhere else: a fact about this disk belongs in the file that runs
// on it.
func (e *Executor) doWrite(s walk.Step) error {
	kind := s.Get("write")
	if kind == "" {
		return fail(s.Name, "`write:` must name a file kind")
	}
	name, err := e.writeTemplate(s, kind)
	if err != nil {
		return err
	}
	e.Driver.Log(fmt.Sprintf("  wrote %s; the scp: step after this copies it in", name))
	return nil
}

// writeTemplate renders one bootstrap file into the work directory and returns its name.
//
// IT IS SHARED WITH `scp:`'s `template:` ARGUMENT, because `write:` AND THE `scp:` AFTER IT WERE
// ALWAYS ONE OPERATION. Measured over the tree: all 16 `write:` steps are followed immediately by
// the `scp:` that copies the same file in, so a file said "make this" and then "copy the thing you
// just made" - two names for one fact, and a reader cannot see the file's content without opening
// Go. `scp: {template: launcher, to: /tmp/x}` is one step that says both.
func (e *Executor) writeTemplate(s walk.Step, kind string) (string, error) {
	family := s.Get("family")
	if family == "" {
		family = s.Get("os")
	}
	if family == "" && e.File != nil {
		for _, other := range e.File.Steps {
			if v := other.Get("os"); v != "" {
				family = v
				break
			}
		}
	}

	opts := bootstrap.Options{
		Family: bootstrap.Family(family),
		Host:   "172.30.0.1",
		Share:  "shared",
		User:   "guest",
	}
	if e.File != nil {
		if v := e.File.Vars["share_user"]; v != nil {
			opts.User = fmt.Sprint(v)
		}
		if v := e.File.Vars["share"]; v != nil {
			opts.Share = fmt.Sprint(v)
		}
	}

	var body, name string
	switch kind {
	case "launcher":
		body, name = bootstrap.Launcher(opts), bootstrap.LauncherName(opts.Family)
	case "macos_agent_plist":
		body, name = bootstrap.MacOSAgentPlist("$HOME/bootstrap-launcher.sh"),
			"org.vm.bootstrap.plist"
	case "windows_task":
		body, name = bootstrap.WindowsTaskScript(
			"C:/Users/"+e.userForPaths()+"/bootstrap-launcher.ps1"), "bootstrap-install.ps1"
	default:
		return "", fail(s.Name, "`%s` is not a file this build writes; the kinds are "+
			"launcher, macos_agent_plist, windows_task", kind)
	}

	path := filepath.Join(e.Work, name)
	if err := os.MkdirAll(e.Work, 0o755); err != nil {
		return "", fail(s.Name, "%v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", fail(s.Name, "writing %s: %v", name, err)
	}
	return name, nil
}

func (e *Executor) userForPaths() string {
	if e.File != nil && e.File.SSHUser != "" {
		return e.File.SSHUser
	}
	return "vmuser"
}

func (e *Executor) doScreenshot(s walk.Step) error {
	dest := s.Get("screenshot")
	if dest == "" {
		return fail(s.Name, "`screenshot:` must name a path")
	}
	f, err := e.Driver.Frame()
	if err != nil {
		return fail(s.Name, "%v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fail(s.Name, "%v", err)
	}
	src, err := os.ReadFile(f.Path())
	if err != nil {
		return fail(s.Name, "%v", err)
	}
	if err := os.WriteFile(dest, src, 0o644); err != nil {
		return fail(s.Name, "%v", err)
	}
	e.Driver.Log(fmt.Sprintf("  screenshot: %s", dest))
	// A `screenshot:` IS THE OTHER WAY A PICTURE ENTERS THE LIBRARY, and its name is the file's own
	// base name: a file that writes `out/login.png` has asked for a picture called `login.png`, and
	// that is what a report should call it rather than a step ordinal.
	w, h := f.Size()
	e.storePicture(dest, filepath.Base(dest), "screenshot", w, h)
	return nil
}
func (e *Executor) doActions(s walk.Step) error {
	raw, ok := s.Spec["do"].([]any)
	if !ok || len(raw) == 0 {
		return fail(s.Name, "`do:` must be a list of actions")
	}
	for i, item := range raw {
		spec, ok := item.(map[string]any)
		if !ok {
			return fail(s.Name, "`do:` item %d is not a mapping", i+1)
		}
		// EACH ACTION IS A STEP WITH THE PARENT'S NAME AND ITS OWN KEYS, so `at:`, `settle:`, `key:`,
		// `type:`, `until:` and the rest all work without a second dispatcher.
		sub := walk.Step{
			Index: s.Index,
			Name:  fmt.Sprintf("%s [%d]", s.Name, i+1),
			Spec:  make(map[string]any, len(spec)+1),
		}
		for k, v := range spec {
			sub.Spec[k] = v
		}
		if err := e.runOne(sub); err != nil {
			return err
		}
	}
	return e.settle(s)
}

// resSwitchBudget is how long a desktop at the wrong size is given to switch to the right one.
//
// IT IS NOT THE `timeout:` OF THE STEP, DELIBERATELY. The step's timeout is how long the guest may
// take to draw a desktop AT ALL - ninety minutes for a live medium that unpacks itself from a
// multi-gigabyte ISO. The mode switch happens once the panel is up, and a session that has drawn its
// panel and stayed at 800x600 has no other mode to offer. Two minutes is generous for the ones that
// do switch and bounded for the ones that never will.
const resSwitchBudget = 2 * time.Minute

func resolutionOf(f *walk.File) [2]int {
	if f != nil && f.Resolution[0] > 0 && f.Resolution[1] > 0 {
		return f.Resolution
	}
	return [2]int{1920, 1080}
}

// wakeTheScreen nudges the guest with a click so a blanked display comes back.
//
// IT MOVES EACH TIME, because a click at exactly the same place twice is a double-click to some
// toolkits and would open whatever is under it. The x walks a 40-pixel band, which is enough to be a
// different position and small enough to stay on the wallpaper of every desktop here.
//
// THE COST IS ONE CLICK PER POLL, and it is paid by both waits that use it: a `desktop:` polling
// every five seconds and a `disk_settle:` every twenty. That is deliberate - this is what makes an
// unattended wait survive a screen saver, and `keep_alive:` used to make it OPTIONAL while every one
// of its 25 uses said yes.
func wakeTheScreen(e *Executor) {
	x := 400 + int(time.Now().UnixNano()/int64(time.Second))%40
	_ = e.Driver.Click(x, 300)
}

// doWaitUntil waits for a CONDITION ABOUT THE MACHINE, naming what it waited for.
//
// IT IS THE SECOND HALF OF A SHUTDOWN, MADE VISIBLE. `shutdown: ssh` used to send the request AND
// wait for qemu in one key - and its request was refused (no `sudo`), so the wait then spent its
// whole grace on a machine nobody had asked to stop. Once the request is an `ssh:` step the file
// shows, the waiting has to be a step too, or it hides inside whichever step reads the disk next.
//
// THE CONDITION IS A NAME, NOT A BOOLEAN EXPRESSION: `stopped` is the one that exists, and adding a
// second is a decision in this switch rather than a string a file can invent.
func (e *Executor) doWaitUntil(s walk.Step) error {
	what := s.Get("wait_until")

	// A CEILING IS REQUIRED, NOT DEFAULTED. "How long is too long" is a fact about the guest and the
	// step, so the file says it - a default would be a number nobody chose, and the failure it
	// produces ("still running after 5m0s") would name a budget the author never set.
	if !s.Has("timeout") {
		return fail(s.Name, "`wait_until: %s` needs a `timeout:`. A wait with no ceiling that is "+
			"never satisfied hangs the stage until the runner's own limit, and the error then names "+
			"the runner rather than this step", what)
	}
	limit := seconds(s, "timeout", 0)
	poll := 2 * time.Second
	if v, err := s.Float("poll", 0); err == nil && v > 0 {
		poll = time.Duration(v * float64(time.Second))
	}

	// THE CONDITION IS A NAME IN THE MACHINE'S OWN VOCABULARY, and asking it is the same call an
	// `if:` makes - see machinefact.go. `stopped` is the 1.0 spelling of `machine_stopped`, and it is
	// kept because 32 steps in this tree write it.
	name := "machine_" + what
	switch what {
	case "stopped", "started", "ssh", "qga", "booted", "console_idle", "disk_idle",
		"recording", "share", "frame_still":
	default:
		return fail(s.Name, "`wait_until: %s` is not a condition this build knows. The ones it "+
			"knows are stopped, started, ssh, qga, booted, console_idle, disk_idle, recording, "+
			"share and frame_still", what)
	}

	// THE FIRST MEASUREMENT IS OUTSIDE THE LOOP, so a condition that is already true costs one
	// snapshot rather than one poll interval.
	start := time.Now()
	var why string
	for {
		done, reason, err := e.machineFact(s, name)
		if err != nil {
			return fail(s.Name, "%v", err)
		}
		if done {
			e.Driver.Log(fmt.Sprintf("  %s (%s)", what, time.Since(start).Round(time.Second)))
			return nil
		}
		why = reason
		if time.Since(start) >= limit {
			// THE FACTS AT THE LAST LOOK ARE THE DIAGNOSIS, so one more snapshot is taken for the
			// message rather than kept from the loop - a stale map would describe a moment that has
			// passed, which is the whole reason the live path exists.
			last := map[string]string{}
			if drv, ok := e.Driver.(interface{ Facts() (map[string]string, error) }); ok {
				if f, err := drv.Facts(); err == nil {
					last = f
				}
			}
			return fail(s.Name, "`wait_until: %s` was never satisfied within %s: %s. The facts at "+
				"the last look were %s", what, limit, why, describeFacts(last))
		}
		time.Sleep(poll)
	}
}

// atLeast reads a fact as a number and compares it to the step's own threshold.
//
// THE NUMBER COMES FROM A STRING because the facts are a string map: they travel through the same
// `/api/state` a person reads, and a number that had to be typed there would have to be formatted
// twice. `minimum:` is the step's own key; without it the comparison cannot be made and the wait is
// refused rather than guessed.
func atLeast(fact string, s walk.Step) bool {
	if fact == "" {
		return false
	}
	have, err := strconv.ParseFloat(fact, 64)
	if err != nil {
		return false
	}
	want, err := s.Float("minimum", 0)
	if err != nil {
		return false
	}
	return have >= want
}

func describeFacts(f map[string]string) string {
	keys := make([]string, 0, len(f))
	for k := range f {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, k+"="+f[k])
	}
	return strings.Join(parts, " ")
}

// doACPI sends one message through QEMU's ACPI path.
//
// IT IS A CHANNEL WITH OPERATIONS, not a method of `shutdown:`. The two are different questions:
// "press the power button" and "how do I stop this machine" produced the same key before, so a file
// could not ask for a RESET without also stopping.
//
// THE WAIT IS NOT HERE. `acpi: power` presses the button and returns; whether the guest ever goes is
// `wait_until: stopped`, which is a separate step a reader can see. Bundling them was what made the
// old `shutdown: ssh` spend five minutes waiting for a machine nobody had asked to stop.
func (e *Executor) doACPI(s walk.Step) error {
	what := s.Get("acpi")
	m := e.Driver.Machine()
	if m == nil {
		return fail(s.Name, "no machine is known; a `boot:` step must come first")
	}
	switch what {
	case "power":
		btn, ok := m.(interface{ PressPowerButton() error })
		if !ok {
			return fail(s.Name, "this driver cannot press the guest's power button")
		}
		if err := btn.PressPowerButton(); err != nil {
			return fail(s.Name, "%v", err)
		}
		e.Driver.Log("  pressed the power button; the guest decides whether to honour it")
		return nil
	case "reset":
		reset, ok := m.(interface{ ResetMachine() error })
		if !ok {
			return fail(s.Name, "this driver cannot reset its machine")
		}
		if err := reset.ResetMachine(); err != nil {
			return fail(s.Name, "%v", err)
		}
		e.Driver.Log("  reset the machine")
		return nil
	}
	return fail(s.Name, "`acpi: %s` is not an ACPI message this build sends; the ones it sends are "+
		"`power` (the power button) and `reset`", what)
}

// doQGA runs one qemu-guest-agent command.
//
// THE AGENT IS THE ONLY CHANNEL THAT NEEDS NEITHER SSHD NOR AN ACPI HANDLER, which is why it is the
// one that works on a guest with no network and no login: a macOS install with Remote Login off
// answers the power button with a dialog and has nothing else to ask.
func (e *Executor) doQGA(s walk.Step) error {
	command := s.Get("qga")
	m := e.Driver.Machine()
	if m == nil {
		return fail(s.Name, "no machine is known; a `boot:` step must come first")
	}
	agent, ok := m.(interface {
		GuestAgent(string, map[string]any) (string, error)
	})
	if !ok {
		return fail(s.Name, "this driver has no guest agent channel")
	}

	args := map[string]any{}
	for _, key := range []string{"argv", "path", "body", "mode", "timeout"} {
		if v, ok := s.Spec[key]; ok {
			args[key] = v
		}
	}
	out, err := agent.GuestAgent(command, args)
	if err != nil {
		return fail(s.Name, "`qga: %s`: %v", command, err)
	}
	if out != "" {
		for _, line := range headAndTail(out, 12, 6) {
			e.Driver.Log("  " + line)
		}
	} else {
		e.Driver.Log(fmt.Sprintf("  qga: %s ok", command))
	}
	return nil
}

// rectOfDesktop reads a `desktop:` step's `rect:`, in the three forms the vocabulary allows: a pixel
// quadruple, a fraction (`middle`), or a named band (`bottom 40px`).
//
// A PIXEL RECT IS SCALED AGAINST `rect_at:`, the same way `check:`'s is - the reference is a picture
// of ONE screen size, and a guest that draws at another needs the same place on the page.
func rectOfDesktop(s walk.Step) ([4]int, bool) {
	raw, ok := s.Spec["rect"]
	if !ok {
		return [4]int{}, false
	}
	if v, ok := raw.([]any); ok && len(v) == 4 {
		var r [4]int
		for i := 0; i < 4; i++ {
			f, ok := toFloat(v[i])
			if !ok {
				return [4]int{}, false
			}
			r[i] = int(f)
		}
		return r, true
	}
	return [4]int{}, false
}

// cropBoth crops the SAME PROPORTION of two pictures that may be different sizes.
//
// A pixel rect is written against a frame the file measured - `rect: [0, 0, 1920, 120]` - and a guest
// that is still at 800x600 has the same band at 800x50. Scaling the rect by each picture's own width
// is what makes that the same place rather than the corner they share.
func cropBoth(reference, frame image.Image, r [4]int) (image.Image, image.Image, error) {
	crop := func(img image.Image, w, h int) (image.Image, error) {
		b := img.Bounds()
		fw, fh := b.Dx(), b.Dy()
		if w <= 0 {
			w = fw
		}
		if h <= 0 {
			h = fh
		}
		x0 := r[0] * fw / w
		y0 := r[1] * fh / h
		x1 := r[2] * fw / w
		y1 := r[3] * fh / h
		if x1 <= x0 {
			x1 = x0 + 1
		}
		if y1 <= y0 {
			y1 = y0 + 1
		}
		return screen.Crop(img, [4]int{x0, y0, x1, y1}, 1)
	}
	// The reference's own size is the rect's coordinate space, so it is the one whose numbers do not
	// move; the frame's band is the same proportion of whatever it arrived at.
	rb := reference.Bounds()
	a, err := crop(reference, rb.Dx(), rb.Dy())
	if err != nil {
		return nil, nil, err
	}
	b, err := crop(frame, rb.Dx(), rb.Dy())
	if err != nil {
		return nil, nil, err
	}
	return a, b, nil
}

// normalisePair scales two pictures to the smaller of their sizes, so a comparison can run at all.
//
// NEAREST NEIGHBOUR, and only down: this runs once per poll on a picture that is about to be reduced
// to a fraction of changed pixels, so an interpolated edge would add grey that no threshold asked for.
func normalisePair(a, b image.Image) (image.Image, image.Image) {
	aw, ah := a.Bounds().Dx(), a.Bounds().Dy()
	bw, bh := b.Bounds().Dx(), b.Bounds().Dy()
	w, h := aw, ah
	if bw < w {
		w = bw
	}
	if bh < h {
		h = bh
	}
	scale := func(img image.Image) image.Image {
		out := image.NewRGBA(image.Rect(0, 0, w, h))
		ib := img.Bounds()
		if ib.Dx() == w && ib.Dy() == h {
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					out.Set(x, y, img.At(ib.Min.X+x, ib.Min.Y+y))
				}
			}
			return out
		}
		for y := 0; y < h; y++ {
			sy := ib.Min.Y + y*ib.Dy()/h
			for x := 0; x < w; x++ {
				sx := ib.Min.X + x*ib.Dx()/w
				out.Set(x, y, img.At(sx, sy))
			}
		}
		return out
	}
	return scale(a), scale(b)
}

// renderForCopy returns a path to a copy of local with `{{ name }}` substituted, or local unchanged
// when it is binary or has no placeholders.
//
// IT IS DELIBERATELY CONSERVATIVE: only a file that is valid UTF-8 and actually contains `{{` is
// copied. A disk image, an ISO or a .pkg must not be slurped into memory and rewritten, and a file
// with no placeholder has nothing to gain from an extra copy.
func (e *Executor) renderForCopy(s walk.Step, local string) string {
	if e.File == nil {
		return local
	}
	data, err := os.ReadFile(local)
	if err != nil {
		return local
	}
	if !strings.Contains(string(data), "{{") || !utf8.Valid(data) {
		return local
	}
	rendered, err := e.File.Render(string(data))
	if err != nil {
		e.Driver.Log(fmt.Sprintf("  %s: %v (copied unchanged)", filepath.Base(local), err))
		return local
	}
	out, err := os.CreateTemp(filepath.Dir(local), filepath.Base(local)+".rendered-*")
	if err != nil {
		return local
	}
	mode := os.FileMode(0o644)
	if fi, statErr := os.Stat(local); statErr == nil {
		mode = fi.Mode().Perm()
	}
	_, writeErr := out.WriteString(rendered)
	closeErr := out.Close()
	if writeErr != nil || closeErr != nil {
		return local
	}
	_ = os.Chmod(out.Name(), mode)
	e.Driver.Log(fmt.Sprintf("  rendered %s with this file's vars", filepath.Base(local)))
	return out.Name()
}


// qemuArgPairs flattens a boot step's `args:` into the one list qemu is given.
//
// IT REFUSES AN ODD LENGTH, because every qemu flag takes a value: a flat list whose `-device` lost
// its argument is an error naming the flag rather than what was meant, and it arrives at startup
// forty minutes into a stage.
func qemuArgPairs(s walk.Step) []string {
	raw, ok := s.Spec["args"].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range raw {
		switch v := item.(type) {
		case []any:
			for _, one := range v {
				out = append(out, fmt.Sprint(one))
			}
		case string:
			out = append(out, v)
		}
	}
	return out
}

// bootArgs is the qemu argument list a boot step writes, for the "is this the same machine" check.
func bootArgs(s walk.Step) []string {
	out := []string{s.Get("machine")}
	out = append(out, qemuArgPairs(s)...)
	return out
}
