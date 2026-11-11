package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/easydev-org/easyvm/internal/guest"
	"github.com/easydev-org/easyvm/internal/keys"
	"github.com/easydev-org/easyvm/internal/machine"
	easyvmnet "github.com/easydev-org/easyvm/internal/net"
	"github.com/easydev-org/easyvm/internal/overlay"
	"github.com/easydev-org/easyvm/internal/qmp"
	"github.com/easydev-org/easyvm/internal/run"
	"github.com/easydev-org/easyvm/internal/screen"
	"github.com/easydev-org/easyvm/internal/walk"
)

const (
	// DefaultNovNCPort is where a browser connects. 6080 is the noVNC convention.
	DefaultNovNCPort = 6080
	// vncBase is qemu's own numbering: display N listens on 5900+N.
	vncBase = 5900
)

// qemuDriver is the one implementation of run.Driver: it holds the qemu process, the QMP socket and
// the ssh connection, and it starts each of them at most once.
//
// IT LAZILY OPENS EACH CHANNEL, and that is not tidiness. A walkthrough's first step may be
// `record:` before `boot:`, so the QMP socket does not exist yet; the ssh connection cannot be made
// until the guest answers; and a `run` that opened everything up front would fail on a file whose
// steps are in a different order than the implementation expected.
type Driver struct {
	file *walk.File
	work string
	port int
	pass string

	// novncPort is where the browser page listens; 0 disables it.
	novncPort int

	mu       sync.Mutex
	proc     *exec.Cmd
	qmpReg   *qmp.Registry
	qmpConn  *qmp.Conn
	ssh      *guest.Client
	diskPath string
	// diff is the throwaway overlay this run wrote into, when it made one. Its Close is what
	// proves the base was not written to, so it belongs to the driver rather than to a step.
	diff *overlay.Overlay
	// flattenFrom is the diff a `compress:` step must read instead of diskPath - the same file as
	// diff.Path, kept because the file may already be gone by the time the step asks.
	flattenFrom string
	page        *consoleServer
	vncPort     int
	netUp       bool
	logf        func(string)
}

// NewDriver builds the driver and returns a closer for the caller's defer.
func NewDriver(f *walk.File, work, bootDisk string, port int, password string,
	novncPort int) (run.Driver, func(), error) {
	if err := os.MkdirAll(work, 0o755); err != nil {
		return nil, nil, err
	}
	d := &Driver{
		file:      f,
		work:      work,
		port:      port,
		pass:      password,
		diskPath:  bootDisk,
		novncPort: novncPort,
		logf: func(msg string) {
			fmt.Printf("[easyvm] %s\n", msg)
		},
	}
	return d, func() {
		d.shutdown()
	}, nil
}

func (d *Driver) Log(msg string) { fmt.Println(msg) }

// --- the screen -----------------------------------------------------------------------

// Frame captures the screen through QMP.
func (d *Driver) Frame() (run.Frame, error) {
	conn, err := d.qmpSocket()
	if err != nil {
		return nil, err
	}
	f, err := conn.Screendump(filepath.Join(d.work, "frames"))
	if err != nil {
		return nil, err
	}
	return frame{path: f.Path, w: f.Width, h: f.Height}, nil
}

type frame struct {
	path string
	w, h int
}

func (f frame) Path() string     { return f.path }
func (f frame) Size() (int, int) { return f.w, f.h }
func (f frame) Words() []string  { return nil } // no OCR in this build; see the note in run.Driver

// Click presses and releases at a point.
//
// QMP'S ABSOLUTE POINTER TAKES ONE EVENT PAIR PER AXIS, and the guest's tablet scales them. That is
// why the coordinates are the file's own measured pixels rather than anything the host computes.
func (d *Driver) Click(x, y int) error {
	conn, err := d.qmpSocket()
	if err != nil {
		return err
	}
	return conn.Click(x, y)
}

func (d *Driver) DoubleClick(x, y int) error {
	if err := d.Click(x, y); err != nil {
		return err
	}
	time.Sleep(120 * time.Millisecond)
	return d.Click(x, y)
}

func (d *Driver) Key(qcode string) error {
	conn, err := d.qmpSocket()
	if err != nil {
		return err
	}
	return conn.Key(qcode)
}

func (d *Driver) Stroke(modifiers []string, key string) error {
	conn, err := d.qmpSocket()
	if err != nil {
		return err
	}
	return conn.Stroke(modifiers, key)
}

// Type sends text one keystroke at a time, at the pace the caller measured.
func (d *Driver) Type(text string, perChar time.Duration) ([]rune, error) {
	conn, err := d.qmpSocket()
	if err != nil {
		return nil, err
	}
	strokes, err := keys.ForText(text)
	if err != nil {
		// A CHARACTER WITH NO QCODE IS NOT FATAL, and the caller logs which ones were skipped.
		// Refusing the whole line because one character cannot be named is worse than typing the
		// rest and saying so - that is the contract the Python implementation's caller expected.
		var kept []keys.Stroke
		var skipped []rune
		for _, ch := range text {
			one, oneErr := keys.ForText(string(ch))
			if oneErr != nil {
				skipped = append(skipped, ch)
				continue
			}
			kept = append(kept, one...)
		}
		strokes = kept
		if err := d.sendStrokes(conn, strokes, perChar); err != nil {
			return skipped, err
		}
		return skipped, nil
	}
	return nil, d.sendStrokes(conn, strokes, perChar)
}

func (d *Driver) sendStrokes(conn *qmp.Conn, strokes []keys.Stroke, perChar time.Duration) error {
	for _, s := range strokes {
		if err := conn.Stroke(s.Modifiers, s.Key); err != nil {
			return err
		}
		if perChar > 0 {
			time.Sleep(perChar)
		}
	}
	return nil
}

// --- the guest ------------------------------------------------------------------------

func (d *Driver) Guest() *guest.Client {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ssh != nil {
		return d.ssh
	}
	target := guest.Target{Host: "172.30.0.1", Port: d.port, User: d.file.SSHUser, Password: d.pass}
	client, err := guest.Dial(target, 30*time.Second)
	if err != nil {
		d.logf(fmt.Sprintf("  could not open an ssh connection: %v", err))
		return nil
	}
	d.ssh = client
	return d.ssh
}

// --- the machine ----------------------------------------------------------------------

func (d *Driver) Machine() run.Machine { return machineControl{d} }

// SetDisk is how a `disk:` step tells boot: what to start.
func (d *Driver) SetDisk(path string) {
	d.mu.Lock()
	d.diskPath = path
	d.mu.Unlock()
}

type machineControl struct{ d *Driver }

func (m machineControl) SetDisk(path string) { m.d.SetDisk(path) }

// Disk is what a `compress:` step asks for: the file this stage has been writing to.
//
// IT IS THE DIFF WHEN THERE IS ONE, and that is the whole point of the field next to it. A stage
// that ran on an overlay wrote nothing to the disk it was given - the artifact has to be built
// from the diff, or the published disk is the one the run never touched.
func (m machineControl) Disk() string {
	m.d.mu.Lock()
	defer m.d.mu.Unlock()
	if m.d.flattenFrom != "" {
		return m.d.flattenFrom
	}
	return m.d.diskPath
}

// ReleaseDiff deletes the diff and proves the base was not written to.
//
// IDEMPOTENT, BECAUSE TWO THINGS CALL IT. `compress:` releases the diff after flattening, and the
// driver's own shutdown releases it if the walk failed before that step - and a failed walk is
// exactly when the base has never been re-checked. overlay.Close tolerates a file that is already
// gone, so the second call verifies without deleting.
func (m machineControl) ReleaseDiff() error {
	d := m.d
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.releaseDiffLocked()
}

func (d *Driver) releaseDiffLocked() error {
	if d.diff == nil {
		return nil
	}
	written, err := d.diff.Close()
	if written > 0 {
		d.logf(fmt.Sprintf("  released the diff (%d MiB written this run)", written>>20))
	}
	if err != nil {
		return err
	}
	if d.flattenFrom == d.diff.Path {
		// The flat has been published or the run is over; either way the path must stop being
		// handed to a `compress:` step that would then open a file that does not exist.
		d.flattenFrom = ""
	}
	d.diff = nil
	return nil
}

// Start launches qemu with the ONE command line every family shares.
func (m machineControl) Boot(spec run.BootSpec) error {
	d := m.d
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.proc != nil {
		return fmt.Errorf("a machine is already running")
	}

	guestOS, err := machine.ParseOS(spec.OS)
	if err != nil {
		return err
	}
	// THE ACCELERATOR IS NAMED IN THE COMMAND LINE, AND QEMU IS THE AUTHORITY ON IT.
	//
	// `-accel kvm` IS IN THE ARGUMENTS BELOW, which makes qemu refuse to start rather than silently
	// choose TCG - the silent choice is what put every macOS leg on software emulation, where
	// `+invtsc` cannot be provided and the guest deadlocks at HANDOFF TO XNU.
	//
	// `CheckKVM()` IS DIAGNOSIS, NOT A GATE. It was a gate for one run, and its `KVM_GET_API_VERSION`
	// call passed a pointer where the ioctl takes no argument - so it reported "invalid argument" on a
	// machine whose qemu had just accepted `-accel kvm`. A CHECK THAT CAN REFUSE A GUEST QEMU WOULD
	// HAVE STARTED IS WORSE THAN NO CHECK: the authority is the program that has to work.
	//
	// So a failure here is a LOG LINE - it tells a reader why a boot was slow or why qemu exited -
	// and the boot proceeds to qemu, whose own message is the verdict.
	if err := machine.CheckKVM(); err != nil {
		d.logf("  " + err.Error())
	}
	opts, err := machine.CommonOptions{CPUs: spec.CPUs, MemMB: spec.MemMB, SSHPort: d.port}.
		Normalise()
	if err != nil {
		return err
	}

	// THE BOOT DESCRIPTION GOES THROUGH Validate BEFORE ANY PATH IS USED. A stage that never ran
	// its `fetch:` gets a sentence naming what is missing, not qemu's message about a drive.
	boot := machine.Boot{
		OS:           guestOS,
		Disk:         spec.Disk,
		ISO:          spec.ISO,
		AnswerCD:     spec.AnswerCD,
		DriverCD:     spec.DriverCD,
		Recovery:     spec.Recovery,
		OpenCoreROM:  spec.OpenCoreROM,
		OpenCoreVars: spec.OpenCoreVars,
		BootImage:    spec.BootImage,
	}
	if err := boot.Validate(); err != nil {
		return err
	}

	cpu, err := boot.CPU("")
	if err != nil {
		return err
	}

	// THE DIFF IS MADE BEFORE qemu SEES THE DISK, because what qemu is given IS the diff.
	//
	// AN INSTALL WRITES THE DISK ITSELF (see machine.Boot.WantsOverlay): there is nothing behind a
	// freshly created qcow2 to protect, and a diff would be one more file to flatten. Every other
	// boot reads through a read-only base - which is what makes it safe for the guest to TRIM, and
	// what makes `compress:` a flatten rather than a copy.
	running := spec.Disk
	if boot.WantsOverlay() {
		o, err := overlay.Create(spec.Disk, d.work, "run-overlay.qcow2")
		if err != nil {
			return err
		}
		d.diff = o
		d.flattenFrom = o.Path
		running = o.Path
		d.logf(fmt.Sprintf("  running on a diff over %s (read-only); %s",
			filepath.Base(o.Base), o.Path))
	}
	boot.Disk = running

	qmpPath := filepath.Join(d.work, "drive.sock.qmp")
	_ = os.Remove(qmpPath)

	// THE DISPLAY NUMBER IS FIXED AT 0 INSIDE THE CONTAINER: one guest per container, and a
	// computed number would make the log line and the -vnc argument disagree.
	const vncDisplay = 0
	d.vncPort = vncBase + vncDisplay

	args := []string{
		// THE ACCELERATOR IS NAMED, AND ITS ABSENCE WAS THE REASON EVERY LEG RAN ON TCG.
		//
		// `-machine q35` WITH NO `-accel` LETS QEMU CHOOSE, and qemu's default is `tcg` - pure
		// software emulation. It does not fail, it does not warn in a way that reads as a problem,
		// and the guest boots: slowly. Measured on run 43063744565's macos-setup leg, whose log is
		// full of
		//
		//	qemu-system-x86_64: warning: TCG doesn't support requested feature:
		//	  CPUID[80000007h].EDX.invtsc [bit 8]
		//
		// - `+invtsc` is one of the twenty CPU flags macOS needs, and TCG cannot provide it. That is
		// the deadlock documented in internal/machine: every vCPU at 100%, block I/O frozen, the
		// serial output ending at `[EB|LOG:HANDOFF TO XNU]` with no panic.
		//
		// `-accel kvm` MAKES THE FAILURE LOUD: a container without a usable /dev/kvm now says so at
		// startup instead of running at a fiftieth of the speed and timing out forty minutes later.
		"-machine", "q35,accel=kvm",
		"-cpu", cpu,
		"-smp", boot.SMP(opts.CPUs),
		"-m", fmt.Sprint(opts.MemMB),
		"-vga", boot.VGA(opts.VGA),
		"-display", "none",
		// THE CONSOLE GOES TO A FILE, BECAUSE ONE STEP HAS TO READ IT.
		//
		// A Windows install ISO does not boot by itself. The El Torito boot image prints
		//
		//	Press any key to boot from CD or DVD...
		//
		// and waits about five seconds; nothing pressing it means the firmware falls through to
		// PXE and the run ends with four thousand bytes of firmware output and no Windows. That
		// prompt is in the boot sector, before setup.exe runs, so no answer-file setting can
		// suppress it - the only way past it is a keypress, and the only way to know when is the
		// console.
		//
		// OVMF's own marker comes first and is what the Python implementation actually keys on:
		// `UEFI QEMU ... DVD-ROM` appears seconds before the prompt and in every UEFI boot.
		//
		// `-serial file:` rather than a chardev socket: the reader is a step that polls a file,
		// and a socket would need its own owner.
		"-serial", "file:" + filepath.Join(d.work, "console.log"),
	}
	// THE GUEST'S OWN DEVICES, IN THE ORDER THE GUEST READS THEM. This is where the three-way split
	// between a Windows disk boot, a Windows ISO boot and a macOS boot lives - all of it in
	// internal/machine, none of it here.
	args = append(args, boot.StorageArgs()...)
	args = append(args, boot.SMCArgs("")...)
	args = append(args, boot.ExtraArgs()...)
	args = append(args,
		"-qmp", "unix:"+qmpPath+",server=on,wait=off",
		"-vnc", fmt.Sprintf("127.0.0.1:%d", vncDisplay),
		"-netdev", fmt.Sprintf("tap,id=net0,ifname=%s,script=no,downscript=no",
			easyvmnet.TapName),
		"-device", fmt.Sprintf("virtio-net-pci,netdev=net0,mac=%s,romfile=,host_mtu=%d",
			easyvmnet.TapMAC, easyvmnet.MTU),
		"-device", "virtio-tablet-pci",
	)

	// FIRMWARE. OVMF for Windows and Linux, OpenCore for macOS - and the variables file is COPIED
	// rather than referenced, because qemu writes to it.
	ovmfCode := os.Getenv("OVMF_CODE")
	if ovmfCode == "" {
		ovmfCode = "/usr/share/OVMF/OVMF_CODE_4M.fd"
	}
	ovmfVars := filepath.Join(d.work, "OVMF_VARS.fd")
	tmpl := os.Getenv("OVMF_VARS_TEMPLATE")
	if tmpl == "" {
		tmpl = "/usr/share/OVMF/OVMF_VARS_4M.fd"
	}
	if !guestOS.IsMacOS() {
		if err := machine.TouchOvmfVars(tmpl, ovmfVars); err != nil {
			return err
		}
	}
	args = append(args, boot.FirmwareArgs(ovmfCode, ovmfVars)...)

	// THE NETWORK BEFORE THE MACHINE, because qemu opens the tap itself: a tap that does not exist
	// is `qemu: could not configure /dev/net/tun`, which reads as a qemu problem.
	//
	// Preflight REFUSES WITH A SENTENCE when the container lacks /dev/net/tun or NET_ADMIN, and
	// that sentence is the whole reason it exists - the raw failure is
	// `RTNETLINK answers: Operation not permitted`, which reads as a broken network.
	if err := easyvmnet.Up(d.port, nil); err != nil {
		return err
	}
	d.netUp = true

	cmd := exec.Command("qemu-system-x86_64", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	d.logf("qemu " + strings.Join(args, " "))
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting qemu: %w", err)
	}
	d.proc = cmd
	d.qmpReg = qmp.NewRegistry()

	// THE PAGE, ONCE THE MACHINE IS UP.
	if d.novncPort > 0 {
		page, err := startConsole("127.0.0.1", d.novncPort, d.vncPort, d.logf)
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			d.proc = nil
			return err
		}
		d.page = page
	}

	// WAIT FOR THE SOCKET BEFORE RETURNING, so the next step can act on the machine rather than
	// racing its creation.
	if _, err := d.qmpSocketLocked(); err != nil {
		return err
	}
	return nil
}

// Stop shuts down by the method the file named, and there is no silent fallback between them.
func (m machineControl) Stop(method string) error {
	d := m.d
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.proc == nil {
		return nil
	}
	switch method {
	case "kill":
		d.logf("  killing the guest")
	case "ssh", "guest_agent", "acpi":
		// THIS BUILD HAS NO GRACEFUL PATH YET, and it says so rather than killing quietly. A
		// shutdown that reported success after a kill is the failure the Python implementation
		// removed a whole fallback chain to stop making.
		return fmt.Errorf("shutdown method %q is not implemented in this build; `kill` is", method)
	default:
		return fmt.Errorf("%q is not a shutdown method; use guest_agent, ssh, acpi or kill", method)
	}
	if d.page != nil {
		d.page.stop()
		d.page = nil
	}
	if err := d.proc.Process.Kill(); err != nil {
		return fmt.Errorf("kill: %w", err)
	}
	_ = d.proc.Wait()
	d.proc = nil
	return nil
}

// Reboot resets the machine and waits for it to answer again.
//
// IT RESETS RATHER THAN STOPPING AND STARTING, and that is not an optimisation: a stop-and-start
// gives the guest a cold boot, which for an installer means it starts over. `system_reset` through
// QMP is what `REBOOT` means in every file that asks for one.
func (m machineControl) Reboot() error {
	d := m.d
	conn, err := d.qmpSocket()
	if err != nil {
		return err
	}
	// QEMU'S OWN RESET, NOT A PROCESS RESTART. The command is the machine's, and the guest sees a
	// warm reboot - which is what a stage that says `reboot:` wants, because the alternative is a
	// cold boot that restarts an installer.
	if err := conn.Reset(); err != nil {
		return fmt.Errorf("resetting the machine: %w", err)
	}
	// AND THE CACHED SSH CONNECTION DIES WITH THE GUEST.
	//
	// `Guest()` KEEPS ONE CONNECTION for the whole run, which is right until the machine reboots:
	// after a reset the guest's sshd is gone with its TCP state, so the next `ssh:` step writes into
	// a socket that will never answer and reports
	//
	//	error: read what this boot did: ssh session: EOF
	//
	// Measured on run 43063744565's manjaro and opensuse legs, whose LAST step is an `ssh:` after a
	// `reboot:`. Closing it here means the next step dials fresh, which is the only correct answer:
	// a connection to a machine that restarted is not a connection.
	d.mu.Lock()
	if d.ssh != nil {
		_ = d.ssh.Close()
		d.ssh = nil
	}
	d.mu.Unlock()
	d.logf("  the machine was reset; the ssh connection was closed with it")
	return nil
}

func (m machineControl) IsRunning() bool {
	d := m.d
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.proc != nil
}

// qmpSocket opens the connection, once.
func (d *Driver) qmpSocket() (*qmp.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.qmpSocketLocked()
}

func (d *Driver) qmpSocketLocked() (*qmp.Conn, error) {
	if d.qmpConn != nil {
		return d.qmpConn, nil
	}
	if d.qmpReg == nil {
		d.qmpReg = qmp.NewRegistry()
	}
	path := filepath.Join(d.work, "drive.sock.qmp")
	conn, err := d.qmpReg.Acquire(path, "easyvm", 30*time.Second)
	if err != nil {
		return nil, err
	}
	d.qmpConn = conn
	return conn, nil
}

func (d *Driver) shutdown() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ssh != nil {
		_ = d.ssh.Close()
		d.ssh = nil
	}
	if d.qmpReg != nil {
		_ = d.qmpReg.CloseAll()
		d.qmpReg = nil
		d.qmpConn = nil
	}
	if d.page != nil {
		d.page.stop()
		d.page = nil
	}
	if d.proc != nil {
		_ = d.proc.Process.Kill()
		_ = d.proc.Wait()
		d.proc = nil
	}
	if d.netUp {
		easyvmnet.Down()
		d.netUp = false
	}
	// AND THE DIFF IS RELEASED AND ITS BASE VERIFIED, EVEN WHEN THE WALK FAILED.
	//
	// `compress:` releases it on the success path. A walk that failed before that step never
	// re-checks the base - and a failed walk is exactly when "I did not write to it" is most
	// likely to be false. Close is idempotent, so this is safe after a successful flatten too.
	if err := d.releaseDiffLocked(); err != nil {
		d.logf("  " + err.Error())
	}
}

func copyFile(from, dest string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer dst.Close()
	_, err = dst.ReadFrom(src)
	return err
}

// screenUnused keeps the screen import honest while the frame reader has no OCR: the desktop step
// calls screen.Load directly, and this reference documents that the package is reached from here.
var _ = screen.MinBrightnessDrop
