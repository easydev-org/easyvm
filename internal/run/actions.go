package run

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/easydev-org/easyvm/internal/asset"
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
	// `os:` REPLACED `family:`, and both are read so a file that has not been renamed still runs.
	osName := s.Get("os")
	if osName == "" {
		osName = s.Get("family")
	}
	if osName == "" {
		return fail(s.Name, "a boot step must name `os:` (windows, linux or macos)")
	}

	spec := BootSpec{
		OS:       osName,
		Disk:     e.bootDisk(),
		ISO:      s.Get("iso"),
		AnswerCD: s.Get("answer_cd"),
		DriverCD: s.Get("driver_cd"),
		Recovery: s.Get("recovery"),
	}
	// THE OPENCORE FILES ARE NEEDED BY EVERY MACOS BOOT, AND ONLY SOME OF THEM ATTACH THE
	// RECOVERY IMAGE. They are built from it - Apple's APFS driver comes out of that image - but
	// that is HOST work done by an earlier step, and the guest is not given the image on an
	// installed disk. Tying these three paths to `recovery:` conflated the two, so an installed
	// macOS disk would have booted with an empty pflash path.
	if osName == "macos" {
		spec.OpenCoreROM = filepath.Join(e.Work, "opencore", "opencore.rom")
		spec.OpenCoreVars = filepath.Join(e.Work, "opencore", "vars.fd")
		spec.BootImage = filepath.Join(e.Work, "opencore", "boot.img")
	}

	e.Driver.Log(fmt.Sprintf("  booting %s from %s%s", spec.OS, bootSourceName(spec),
		mediaNote(spec)))
	if err := m.Boot(spec); err != nil {
		return fail(s.Name, "boot: %v", err)
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
	if err := m.Stop(method); err != nil {
		return fail(s.Name, "%v", err)
	}
	return nil
}

func (e *Executor) doReboot(s walk.Step) error {
	m := e.Driver.Machine()
	if m == nil {
		return fail(s.Name, "this driver has no machine to reboot")
	}
	if err := m.Reboot(); err != nil {
		return fail(s.Name, "%v", err)
	}
	e.Driver.Log("  the machine is back")
	return nil
}

// --- the disk -------------------------------------------------------------------------

// doDisk names the file `boot:` will start.
//
// `disk:` IS A PATH, NOT A MAPPING. Every real file writes `disk: disk.qcow2`, and the richer
// {from, copy} form was never used outside a comment. Accepting both would be a second shape for
// one fact, which is the drift this port exists to remove - so a mapping is refused by name rather
// than silently half-read.
//
// THE OVERLAY IS `boot:`'s JOB, and that is from the Python implementation: a file used to need a
// separate `disk_overlay:` step because letting `boot:` make the overlay had no way to say so. Now
// it does, so this step only says which file.
func (e *Executor) doDisk(s walk.Step) error {
	v, ok := s.Spec["disk"]
	if !ok {
		return fail(s.Name, "`disk:` must name a file")
	}
	path, isPath := v.(string)
	if !isPath {
		if _, isMap := v.(map[string]any); isMap {
			return fail(s.Name, "`disk:` names a FILE, not a mapping: write `disk: disk.qcow2`. "+
				"The {from, copy} form was never used by any file in this repository")
		}
		return fail(s.Name, "`disk:` must be a file name, got %T", v)
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

// --- the screen -----------------------------------------------------------------------

func (e *Executor) doDesktop(s walk.Step) error {
	family := s.Get("desktop_family")
	if family == "" {
		family = s.Get("os")
	}
	if family == "" {
		family = "windows"
	}
	timeout := seconds(s, "timeout", 900)
	poll := seconds(s, "poll", 5)

	e.Driver.Log(fmt.Sprintf("  waiting up to %s for a %s desktop", timeout, family))
	start := time.Now()
	deadline := start.Add(timeout)
	var lastStats screen.Stats
	for time.Now().Before(deadline) {
		f, err := e.Driver.Frame()
		if err != nil {
			e.Driver.Log(fmt.Sprintf("  could not read the screen: %v", err))
			time.Sleep(poll)
			continue
		}
		img, err := screen.Load(f.Path())
		if err != nil {
			// A FRAME THAT CANNOT BE READ IS NOT A FAILURE OF THE GUEST. qemu writes it
			// asynchronously, so the newest one can be half-written; the next look will do.
			time.Sleep(poll)
			continue
		}
		var ok bool
		switch family {
		case "windows":
			ok, lastStats = screen.IsWindowsDesktop(img)
		default:
			return fail(s.Name, "the %q desktop test is not implemented in this build", family)
		}
		if ok {
			e.Driver.Log(fmt.Sprintf("  a desktop is on screen (%s); %s",
				time.Since(start).Round(time.Second), lastStats.Explain()))
			return nil
		}
		time.Sleep(poll)
	}
	return failWithScreen(s.Name, e.Driver,
		"no %s desktop within %s; %s", family, timeout, lastStats.Explain())
}

// --- host commands --------------------------------------------------------------------

// doScript writes a body on the host, copies it in and runs it by path.
//
// WHY BY PATH RATHER THAN AS A QUOTED ONE-LINER: the body is the guest's own shell, and a command
// that has to survive python -> ssh -> cmd loses its quotes. That is measured - the Python
// implementation's own comment says the Build Tools `--override` arrived with none of them.
func (e *Executor) doScript(s walk.Step) error {
	client := e.Driver.Guest()
	if client == nil {
		return fail(s.Name, "no ssh connection; a `require_ssh:` step must come first")
	}
	shell := s.Get("script_shell")
	if shell == "" {
		shell = "powershell"
	}
	name := scriptName(s)
	local := filepath.Join(e.Work, name)
	if err := os.MkdirAll(e.Work, 0o755); err != nil {
		return fail(s.Name, "%v", err)
	}
	body := s.Get("script")
	if shell == "cmd" {
		// cmd.exe reads a .bat byte for byte, and a stray \n is a command name.
		body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n")
	}
	if err := os.WriteFile(local, []byte(body), 0o644); err != nil {
		return fail(s.Name, "writing %s: %v", name, err)
	}

	remote := "C:/Users/" + e.File.SSHUser + "/" + name
	if shell == "sh" {
		remote = "/tmp/" + name
	}
	if err := scpFile(client, local, remote); err != nil {
		return fail(s.Name, "copying %s in: %v", name, err)
	}

	var command string
	switch shell {
	case "cmd":
		command = `cmd /c "` + strings.ReplaceAll(remote, "/", `\`) + `"`
	case "powershell", "pwsh":
		command = `powershell -NoProfile -ExecutionPolicy Bypass -File "` + remote + `"`
	default:
		// `bash`, not `sh`: these bodies use `set -uo pipefail` and `[[ ]]`, which POSIX sh does
		// not have and which silently ignores a failing stage of a pipeline.
		command = "bash " + remote
	}

	res, err := client.Run(command, seconds(s, "timeout", 1800))
	e.Driver.Log(fmt.Sprintf("  script %s (%s) -> %d", name, shell, res.Status))
	for _, line := range headAndTail(res.Combined(), 18, 6) {
		e.Driver.Log("    " + line)
	}
	if err != nil {
		return fail(s.Name, "%v", err)
	}
	if res.Status != 0 {
		return fail(s.Name, "the script exited %d", res.Status)
	}
	return nil
}

func (e *Executor) doSCP(s walk.Step) error {
	client := e.Driver.Guest()
	if client == nil {
		return fail(s.Name, "no ssh connection; a `require_ssh:` step must come first")
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
	if err := scpFile(client, local, remote); err != nil {
		return fail(s.Name, "scp %s -> %s: %v", filepath.Base(local), remote, err)
	}
	e.Driver.Log(fmt.Sprintf("  scp: %s -> %s", filepath.Base(local), remote))
	return nil
}

// --- the share ------------------------------------------------------------------------

func (e *Executor) doShareServe(s walk.Step) error {
	dir := s.Get("share_dir")
	if dir == "" {
		dir = "~/shared"
	}
	dir = strings.ReplaceAll(dir, "~", os.Getenv("HOME"))
	e.Driver.Log(fmt.Sprintf("  share: serving %s", dir))
	if sv, ok := e.Driver.(interface{ ServeShare(string) error }); ok {
		if err := sv.ServeShare(dir); err != nil {
			return fail(s.Name, "%v", err)
		}
		return nil
	}
	return notImplemented(s, "share_serve")
}

func (e *Executor) doShareMount(s walk.Step) error {
	family := s.Get("share_family")
	if family == "" {
		family = "windows"
	}
	e.Driver.Log(fmt.Sprintf("  share: the %s guest mounts it", family))
	if mv, ok := e.Driver.(interface{ MountShare(string) error }); ok {
		if err := mv.MountShare(family); err != nil {
			return fail(s.Name, "%v", err)
		}
		return nil
	}
	return notImplemented(s, "share_mount")
}

// --- recording and publishing ---------------------------------------------------------

// doRecord starts or stops a recording.
//
// `record:` IS BOTH, because a file says `record: out/x.mp4` to start and `record: false` to stop.
// The stop half STOPS THE SAMPLER FIRST, so the last second is on disk before the encoder reads -
// stopping the encoder while the sampler still writes gives a video that ends one frame early, and
// on a fast shutdown that is the frame with the work in it.
func (e *Executor) doRecord(s walk.Step) error {
	value := s.Get("record")
	if value == "false" || value == "" {
		e.stopSampler()
		if e.rec == nil {
			return fail(s.Name, "`record: false` but nothing was recording")
		}
		video := s.Get("_video")
		if video == "" {
			video = filepath.Join(e.Work, "install.mp4")
		}
		if err := e.rec.encode(video, e.Driver.Log); err != nil {
			return fail(s.Name, "%v", err)
		}
		e.rec = nil
		return nil
	}
	if err := e.sampler(filepath.Join(e.Work, "shots")); err != nil {
		return fail(s.Name, "%v", err)
	}
	s.Spec["_video"] = value
	e.Driver.Log(fmt.Sprintf("  recording: %s", value))
	return nil
}

// doCompress flattens the disk this stage ran on into the artifact the workflow publishes.
//
// THE PATH IS RELATIVE TO THE REPOSITORY, NOT TO THE WORK DIRECTORY. A workflow writes
// `out/windows11-ssh.qcow2` from where the job started; joining that onto `work` produced
// `out/ssh-work/out/windows11-ssh.qcow2` and the publish step then uploaded nothing. Measured on the
// first real run of this step in the Python implementation.
func (e *Executor) doCompress(s walk.Step) error {
	dest := s.Get("compress")
	if dest == "" {
		return fail(s.Name, "`compress:` must name the artifact path")
	}
	source := e.Driver.Machine()
	if source == nil {
		return fail(s.Name, "no machine is known; a `boot:` step must come first")
	}
	disk := ""
	if d, ok := source.(interface{ Disk() string }); ok {
		disk = d.Disk()
	}
	if disk == "" {
		return fail(s.Name, "no disk is known; a `disk:` step must come first")
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
	passive := map[string]bool{"name": true, "optional": true, "timeout": true, "requires": true,
		"settle": true, "poll": true, "os": true, "family": true}
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
	family := s.Get("family")
	if family == "" {
		family = s.Get("os")
	}
	if family == "" && e.File != nil {
		// The walkthrough's own os, from its first boot step, if it names one.
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
	case "macos_mount_script":
		body, name = bootstrap.MacOSMountScript(opts), "vm-smbmount.sh"
	case "macos_mount_plist":
		body, name = bootstrap.MacOSMountPlist("/usr/local/bin/vm-smbmount.sh"),
			"org.vm.smbmount.plist"
	case "windows_task":
		body, name = bootstrap.WindowsTaskScript(
			"C:/Users/"+e.userForPaths()+"/bootstrap-launcher.ps1"), "bootstrap-install.ps1"
	default:
		return fail(s.Name, "`write: %s` is not a file this build writes; the kinds are "+
			"launcher, macos_agent_plist, macos_mount_script, macos_mount_plist, windows_task", kind)
	}

	path := filepath.Join(e.Work, name)
	if err := os.MkdirAll(e.Work, 0o755); err != nil {
		return fail(s.Name, "%v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return fail(s.Name, "writing %s: %v", name, err)
	}
	// THE PATH IS LOGGED because the `scp:` that follows names it, and a mismatch between the two
	// is the failure this split exists to make visible.
	e.Driver.Log(fmt.Sprintf("  wrote %s (%d bytes); the scp: step after this copies it in",
		path, len(body)))
	return nil
}

func (e *Executor) userForPaths() string {
	if e.File != nil && e.File.SSHUser != "" {
		return e.File.SSHUser
	}
	return "vmuser"
}

// doFetch produces a host-side asset.
func (e *Executor) doFetch(s walk.Step) error {
	kind := s.Get("fetch")
	if kind == "" {
		return fail(s.Name, "`fetch:` must name an asset")
	}
	family := s.Get("family")
	if family == "" && e.File != nil {
		family = string(e.File.SSHUser)
		for _, other := range e.File.Steps {
			if v := other.Get("os"); v != "" {
				family = v
				break
			}
		}
	}
	res, err := asset.Fetch(asset.Kind(kind), family, e.Work)
	if err != nil {
		return fail(s.Name, "%v", err)
	}
	e.Driver.Log(fmt.Sprintf("  ready for the copy: %s", res.Main))
	for _, extra := range res.Extra {
		e.Driver.Log(fmt.Sprintf("  also: %s", extra))
	}
	return nil
}

// --- macOS's own steps ----------------------------------------------------------------

// doWaitFor waits for a control with no readable title.
//
// THE LOGIN WINDOW'S ONLY TEXT IS THE PASSWORD FIELD'S PLACEHOLDER, twelve pixels tall in a 165x40
// box. A whole-frame read returns NOTHING on a finished Setup Assistant, which is why a walk whose
// gate lists fifteen pane titles waits out its entire budget on a screen that is already done.
func (e *Executor) doWaitFor(s walk.Step) error {
	what := s.Get("wait_for")
	if what != "login_window" {
		return fail(s.Name, "`wait_for:` knows login_window only; got %q", what)
	}
	timeout := seconds(s, "timeout", 240)
	poll := seconds(s, "poll", 3)

	e.Driver.Log(fmt.Sprintf("  waiting up to %s for the login window", timeout))
	start := time.Now()
	for time.Since(start) < timeout {
		f, err := e.Driver.Frame()
		if err != nil {
			time.Sleep(poll)
			continue
		}
		img, err := screen.Load(f.Path())
		if err != nil {
			time.Sleep(poll)
			continue
		}
		if screen.HasLoginWindow(img) {
			e.Driver.Log(fmt.Sprintf("  the login window is up (%s)",
				time.Since(start).Round(time.Second)))
			return nil
		}
		time.Sleep(poll)
	}
	// THE CEILING IS NOT A FAILURE *WHEN THE FILE SAYS optional*. `wait_for` returning an error
	// either way, with the tolerance baked in, made the file's own `optional:` a decoration - and
	// the two disagreed about who decided. The decision is the file's, so this reports and lets
	// the walk decide.
	return fmt.Errorf("%s: no login window within %s", s.Name, timeout)
}

// doScreenshot saves the screen, for a step that asks for the picture rather than a failure taking
// one.
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
	return nil
}

// doDemoScript writes a throwaway script, for the primitives walkthrough.
func (e *Executor) doDemoScript(s walk.Step) error {
	if s.Get("demo_script") == "" {
		return fail(s.Name, "`demo_script:` must name the file to write")
	}
	return notImplemented(s, "demo_script")
}
