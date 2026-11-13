package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
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
	// guestOS is the family the machine was actually STARTED as, remembered at boot.
	//
	// IT IS NOT READ FROM THE FILE, because a sandbox runs several files against one machine and the
	// file in hand may not be the one that booted it. What is true is what was passed to qemu.
	guestOS string
	port    int
	pass    string

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
	// share is the smbd this driver started, if a `share_serve:` step asked for one.
	share *shareServer
	logf  func(string)
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

// The guest's screen, and the default is FHD because every coordinate in this repository was
// measured on one.
//
// `-device VGA` IS THE ONLY FORM THAT TAKES `xres`/`yres`: `-vga std` cannot, and a guest given it
// comes up at qemu's own 1280x800 - which put every click on a different screen and failed
// `linux-nixos-ssh` with "`region_at` is [1920, 1080] but this frame is 1280x800".
//
// `vgamem_mb=32` IS NOT TUNING. The adapter's default is 8 MiB, and 1920x1080 at 32 bits per pixel
// is 7.91 MiB BEFORE double buffering - so the default is just too small, the adapter cannot hold a
// frame, and the symptom is a black screen from the first capture with no error at all. 32 MiB is
// four frames of headroom.
const DefaultVideoDevice = "VGA,xres=1920,yres=1080,vgamem_mb=32"

// --- the guest ------------------------------------------------------------------------

func (d *Driver) Guest() *guest.Client {
	// THE LOCK IS HELD TO LOOK AND TO STORE, NEVER TO DIAL.
	//
	// A dial to a guest that is not answering takes the full thirty seconds, and holding `d.mu` across
	// it blocks every other caller of this driver - `status`, `Frame`, the console page - for exactly
	// as long. The same shape deadlocked `Stop`: a lock around a network round trip is wrong even
	// when nothing recurses.
	d.mu.Lock()
	if d.ssh != nil {
		existing := d.ssh
		d.mu.Unlock()
		return existing
	}
	target := guest.Target{Host: "172.30.0.1", Port: d.port, User: d.file.SSHUser, Password: d.pass}
	d.mu.Unlock()

	client, err := guest.Dial(target, 30*time.Second)
	if err != nil {
		d.logf(fmt.Sprintf("  could not open an ssh connection: %v", err))
		return nil
	}

	// AND A CONNECTION THAT ARRIVED WHILE THIS ONE WAS DIALING WINS. Two steps that both needed a
	// guest would otherwise leave two connections, and only the last one would ever be closed.
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ssh != nil {
		_ = client.Close()
		return d.ssh
	}
	d.ssh = client
	return d.ssh
}

// DropGuest closes the cached ssh connection, so the next step dials a new one.
//
// IT IS CALLED BY `require_ssh:` AFTER A SUCCESSFUL PROBE. The probe always makes its own connection;
// the driver keeps one for the run; and a machine that restarted - by `reboot:` or by the guest's own
// `systemctl reboot` inside an `ssh:` command - leaves the cached one writing into a dead socket.
// Measured as `ssh session: EOF` on the step after the probe.
func (d *Driver) DropGuest() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ssh != nil {
		_ = d.ssh.Close()
		d.ssh = nil
	}
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
	d.guestOS = spec.OS
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
		Incoming:     spec.Incoming,
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
	// THE QEMU GUEST AGENT'S PORT, WHICH WAS MISSING ENTIRELY.
	//
	// `vm-configure` INSTALLS `qemu-guest-agent` AND THEN PROVES IT IS RUNNING, and the proof failed:
	//
	//	prove the guest agent is running: the command exited 3
	//
	// THREE IS `systemctl is-active`'s "inactive". The agent was installed and could not start, because
	// the device it talks on does not exist: `/dev/virtio-ports/org.qemu.guest_agent.0` is created by a
	// `virtserialport` on a `virtio-serial` bus, and neither was in this command line. The Python
	// implementation has both, behind `shape.qga_socket`.
	//
	// `server=on,wait=off` SO QEMU STARTS WITHOUT A CLIENT: the host connects when it wants to ask
	// something, and a guest with no host attached is normal.
	//
	// macOS GETS `isa-serial` RATHER THAN THE VIRTIO BUS, and that is not a preference: Apple's own
	// agent never loads under QEMU, and the third-party one that does probes `/dev/cu.serial1` first -
	// which is the FIRST isa-serial device qemu adds, so this must be the only one and it is.
	qgaSocket := filepath.Join(d.work, "qga.sock")
	_ = os.Remove(qgaSocket)
	if guestOS.IsMacOS() {
		args = append(args,
			"-chardev", "socket,id=qga0,path="+qgaSocket+",server=on,wait=off",
			"-device", "isa-serial,chardev=qga0",
		)
	} else {
		args = append(args,
			"-chardev", "socket,id=qga0,path="+qgaSocket+",server=on,wait=off",
			"-device", "virtio-serial",
			"-device", "virtserialport,chardev=qga0,name=org.qemu.guest_agent.0",
		)
	}

	// THE VIDEO ADAPTER, AND `-vga std` CANNOT CARRY A RESOLUTION.
	//
	// EVERY WALKTHROUGH IN THIS REPOSITORY WAS MEASURED AT 1920x1080, and a guest given `-vga std`
	// comes up at qemu's own default - 1280x800 - so every coordinate is read on a different screen.
	// That is not hypothetical: `linux-nixos-ssh` failed with
	//
	//	wait for the Console result: `region_at` is [1920, 1080] but this frame is 1280x800
	//
	// Only the `-device VGA` form takes `xres`/`yres`. `vgamem_mb` IS NOT OPTIONAL AT THIS SIZE: the
	// default is 8 MiB and 1920x1080 at 32 bits is 7.91 MiB before double buffering, so the adapter
	// comes up unable to hold a frame - and the symptom is a black screen from the first capture,
	// with no error.
	//
	// MACOS IS UNCHANGED: it passes `vga: none` because OpenCore's guest has `vmware-svga` as a PCIe
	// -device from `machine.ExtraArgs`, and a second adapter would give the guest two.
	if boot.VGA(opts.VGA) == "none" {
		args = append(args, "-vga", "none")
	} else {
		args = append(args, "-device", DefaultVideoDevice)
	}
	args = append(args, boot.StorageArgs()...)
	args = append(args, boot.SMCArgs("")...)
	args = append(args, boot.ExtraArgs()...)
	args = append(args,
		"-qmp", "unix:"+qmpPath+",server=on,wait=off",
		"-vnc", fmt.Sprintf("127.0.0.1:%d", vncDisplay),
		"-netdev", fmt.Sprintf("tap,id=net0,ifname=%s,script=no,downscript=no",
			easyvmnet.TapName),
		"-device", fmt.Sprintf("virtio-net-pci,netdev=net0,mac=%s,romfile=,host_mtu=%d",
			easyvmnet.TapMAC, easyvmnet.UplinkMTU()),
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

	// A RESUME LOADS INSTEAD OF BOOTING, and `-incoming` is what says so.
	//
	// IT GOES LAST BECAUSE IT IS NOT A DEVICE - qemu reads the whole machine first and then loads the
	// state into it, so the device set must already be described. That is also why the arguments
	// before this line must match the ones the snapshot was taken with: a different cpu, a different
	// machine type or a different device set is refused with
	//
	//	load of migration failed: Unknown section or instance 'apic' 2
	//
	// which names a section rather than the argument that differed.
	if boot.Incoming != "" {
		args = append(args, "-incoming", "file:"+boot.Incoming)
	}

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
	//
	// AND WHEN IT DOES NOT APPEAR, THE QUESTION IS WHY QEMU IS NOT THERE. A QMP socket error is a
	// SOCKET error - `connect: connection refused`, or a thirty-second wait that says nothing - and the
	// reason qemu never created the socket is in QEMU'S OWN OUTPUT, which the caller cannot see:
	//
	//	-device usb-storage,bus=xhci.0,drive=rec: Bus 'xhci.0' not found
	//
	// Measured on the first macos-setup run that got past OpenCore. qemu printed that and exited, and
	// the step reported `qmp .../drive.sock.qmp: connect: connection refused (waited 30s; is another
	// process already holding this socket?)` - a hypothesis about the socket, pointing a reader at
	// qmp.Registry, while the machine had never started.
	//
	// SO THE PROCESS IS CHECKED FIRST. If it is gone, that is the answer and it is reported as such,
	// with qemu's exit status attached.
	if _, err := d.qmpSocketLocked(); err != nil {
		if d.proc != nil {
			if waitErr := d.proc.Process.Signal(syscall.Signal(0)); waitErr != nil {
				exit := "unknown"
				if d.proc.ProcessState != nil {
					exit = d.proc.ProcessState.String()
				}
				return fmt.Errorf("qemu exited before its QMP socket appeared (%s): %w. "+
					"qemu's own output is above - a missing -device, a bad -machine option or an "+
					"accelerator it cannot use all say so there and then exit", exit, err)
			}
		}
		return err
	}
	return nil
}

// Stop shuts down by the method the file named, and there is no silent fallback between them.
func (m machineControl) Stop(method string) error {
	d := m.d
	// THE LOCK IS TAKEN TO LOOK, NOT TO WORK.
	//
	// MEASURED ON UBUNTU-VM: the leg reported `stop the machine` and then sat in `follow it` for
	// thirty minutes with `status` still saying `running`. `Stop` held `d.mu` for its whole body, and
	// the first thing a graceful shutdown does is `d.Guest()` - which takes `d.mu` itself. Same
	// goroutine, same mutex, no way out: the stage had finished every step it exists to do and could
	// never report it.
	//
	// A LOCK AROUND A NETWORK ROUND TRIP WOULD BE WRONG EVEN IF IT DID NOT DEADLOCK: `poweroff` waits
	// on a guest, and every other caller of this driver - `status`, `Frame`, the console - would be
	// blocked behind it for up to five minutes.
	d.mu.Lock()
	running := d.proc != nil
	d.mu.Unlock()
	if !running {
		return nil
	}
	killNow := false
	switch method {
	case "kill":
		// A KILL IS A KILL, AND IT MUST NOT WAIT FIRST. The placeholder here only logged, and the
		// `waitForExit` below then waited the full `shutdownTimeout` for a guest nobody had asked to
		// power off - five minutes per stage whose file says `shutdown: kill`, which is every
		// `windows-ssh` and every throwaway overlay here.
		d.logf("  killing the guest")
		killNow = true
	case "ssh":
		// THE GUEST SHUTS ITSELF DOWN, AND THE FILESYSTEM IT IS ABOUT TO PUBLISH IS CLOSED.
		//
		// This is the method every Linux and BSD `vm-configure` file names, and it was not
		// implemented - so a stage that had done all its work failed on its LAST step:
		//
		//	stop the machine: shutdown method "ssh" is not implemented in this build; `kill` is
		//
		// A KILL IS NOT A SUBSTITUTE HERE. The disk this stage produces is the artifact, and a killed
		// guest leaves a filesystem that has to replay a journal on the next boot - which reads, in
		// the next stage's log, as a slow boot rather than as a torn disk.
		if err := d.shutdownOverSSH(); err != nil {
			return err
		}
	case "guest_agent":
		if err := d.shutdownOverAgent(); err != nil {
			return err
		}
	case "acpi":
		// THE POWER BUTTON, WHICH SOME GUESTS ANSWER AND SOME PROMPT ON. Windows and Linux shut down
		// from it; macOS puts up "Restart / Sleep / Cancel / Shut Down" and waits for a click, which
		// is why every macOS file names `ssh` or `guest_agent` instead.
		if err := d.shutdownOverACPI(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%q is not a shutdown method; use guest_agent, ssh, acpi or kill", method)
	}
	// AND THE PROCESS IS WAITED FOR, WHICHEVER PATH ASKED IT TO STOP. A guest that was asked politely
	// has to actually exit, or the disk is still open when the next step reads it.
	//
	// A KILL SKIPS THE WAIT: there is nothing to be patient with, the process is being taken down
	// because the file said so or because a graceful request expired.
	var err error
	if killNow {
		err = d.killAndReap()
	} else {
		err = d.waitForExit(shutdownTimeout)
	}
	if err != nil {
		return err
	}
	d.mu.Lock()
	page := d.page
	d.page = nil
	d.mu.Unlock()
	if page != nil {
		page.stop()
	}
	// `waitForExit` ALREADY REAPED IT - CALLING `Wait` AGAIN BLOCKS FOREVER.
	//
	// Measured on ubuntu-vm: the leg sat in `follow it` for thirty minutes with `status` still saying
	// `running`, on a machine that had been asked to power off. `waitForExit` calls `d.proc.Wait()`
	// in a goroutine and returns when it comes back; the SECOND `Wait()` below it blocks on an
	// `os.Process` that has already been reaped, and Go's `Wait` does not return an error for that - it
	// waits on a channel nothing will ever close.
	//
	// THE KILL IS ALSO `waitForExit`'S JOB NOW: it returns after reaping, or kills and returns. What
	// is left here is closing the page and clearing the field.
	d.mu.Lock()
	d.proc = nil
	d.mu.Unlock()
	return nil
}

// shutdownTimeout is how long a guest is given to power off after being asked.
//
// THE PYTHON IMPLEMENTATION USED 60 AND RAISED IT TO 300, with a measurement behind it: macOS writes
// its logs, unmounts the share and closes APFS, and a 60-second wait expired on a disk that was doing
// exactly what it should. What happens on expiry is a kill, and the disk then needs a journal replay.
const shutdownTimeout = 300 * time.Second

// shutdownOverSSH asks the guest to power off through its own sshd.
func (d *Driver) shutdownOverSSH() error {
	client := d.Guest()
	if client == nil {
		return fmt.Errorf("`shutdown: ssh` needs a connection, and there is none. A `require_ssh:` " +
			"step must have succeeded first - or use `shutdown: kill` on a throwaway overlay")
	}
	d.logf("  asking the guest to shut down over ssh")
	// `poweroff` RATHER THAN `shutdown -h now`: both are the same request to systemd, and `poweroff`
	// is in /usr/bin on every distribution here while `shutdown` lives in /sbin in some of them.
	//
	// THE CONNECTION DIES AS THE GUEST GOES, SO AN ERROR HERE IS EXPECTED. What matters is that the
	// request was delivered, which is why the failure is logged rather than returned.
	if res, err := client.Run("poweroff", 30*time.Second); err != nil {
		d.logf(fmt.Sprintf("  the connection closed as the guest went down (%v)", err))
		_ = res
	}
	return nil
}

// shutdownOverAgent asks the guest's own qemu-guest-agent to power off.
func (d *Driver) shutdownOverAgent() error {
	sock := filepath.Join(d.work, "qga.sock")
	if _, err := os.Stat(sock); err != nil {
		return fmt.Errorf("`shutdown: guest_agent` needs the agent's socket at %s: %v. The guest "+
			"must have qemu-guest-agent installed AND running - `prove the guest agent is running` "+
			"is the step that checks it", sock, err)
	}
	d.logf("  asking the guest agent to shut down")
	if err := qgaShutdown(sock); err != nil {
		return fmt.Errorf("guest agent shutdown: %w", err)
	}
	return nil
}

// shutdownOverACPI presses the power button through QMP.
func (d *Driver) shutdownOverACPI() error {
	conn, err := d.qmpSocket()
	if err != nil {
		return err
	}
	d.logf("  pressing the power button")
	return conn.SystemPowerdown()
}

// killAndReap takes the machine down now and returns once it is gone.
//
// IT REAPS THE WAIT, so the next `stop` or `start` does not find a zombie whose exit status nobody
// collected - and so the disk is closed before the step that publishes it reads the file.
func (d *Driver) killAndReap() error {
	d.mu.Lock()
	proc := d.proc
	d.mu.Unlock()
	if proc == nil {
		return nil
	}
	if err := proc.Process.Kill(); err != nil {
		return fmt.Errorf("kill: %w", err)
	}
	_ = proc.Wait()
	d.logf("  the guest was killed")
	return nil
}

// waitForExit waits for qemu to leave, and kills it if it does not.
func (d *Driver) waitForExit(limit time.Duration) error {
	// THE PROCESS IS TAKEN ONCE, UNDER THE LOCK, AND THE WAIT RUNS ON THAT VALUE.
	//
	// `Stop` clears `d.proc` when it is done, and `status` reads it - so a wait that dereferenced the
	// field from a goroutine would race with both. One local pointer means this function owns what it
	// is waiting for, whatever else happens to the field.
	d.mu.Lock()
	proc := d.proc
	d.mu.Unlock()
	if proc == nil {
		return nil
	}
	done := make(chan struct{})
	go func() {
		_ = proc.Wait()
		close(done)
	}()
	select {
	case <-done:
		d.logf("  the guest powered off")
		return nil
	case <-time.After(limit):
		// THE EXPIRY IS REPORTED, NOT HIDDEN. A guest that did not power off in five minutes is a
		// guest whose disk will need a journal replay, and the next stage's "slow boot" is this line.
		d.logf(fmt.Sprintf("  the guest did not power off within %s; killing it - the disk will need "+
			"a journal replay on its next boot", limit))
		// AND IT IS KILLED AND REAPED HERE, because the goroutine above is still blocked on `Wait`
		// and this function is the only place that knows the guest missed its deadline. A kill with
		// no reap is a zombie holding the disk open.
		_ = proc.Process.Kill()
		<-done
		return nil
	}
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

// OS is the family this driver was prepared for, so a `boot:` step can tell a no-op from a mistake.
//
// THE FILE'S OWN `os:` IS THE ANSWER, because the driver was built from that file. A second YAML
// naming a different guest against this machine is a question with no good answer - one daemon holds
// one machine - and saying so is better than letting a click land on the wrong screen.
func (m machineControl) OS() string {
	d := m.d
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.guestOS
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
	// THE SHARE GOES WITH THE MACHINE. It is one smbd per run, started by a step, and a daemon that
	// exits without stopping it leaves a server bound to a bridge that is about to be removed.
	d.stopShare()
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

// --- snapshots ---------------------------------------------------------------------
//
// SEE THE NOTE IN package qmp FOR WHY THIS IS A MIGRATION AND NOT `savevm`. The short version: the
// guests here are UEFI, their NVRAM is a pflash device, and `savevm` requires every writable block
// device to support internal snapshots. A migration stream asks nothing of them.
//
// MEASURED ON ALL THREE GUESTS: Linux 2.7 GiB, Windows 12 GiB (unsettled) / 3.7 GiB (settled),
// macOS 5.3 GiB, from an 8 GiB guest. The size is NOT a fixed multiple of RAM - it depends on how
// many pages are zero or compressible at the moment the snapshot is taken, which is why a caller
// must be told the file it got rather than a number it can predict.

// Snapshot writes the machine's whole state to path.
//
// THE MACHINE MUST BE RUNNING. A stopped qemu has no state to save, and a caller that asked is told
// so rather than given an empty file.
func (d *Driver) Snapshot(path string) error {
	d.mu.Lock()
	running := d.proc != nil
	d.mu.Unlock()
	if !running {
		return fmt.Errorf("no machine is running; a `snapshot:` step needs a `boot:` before it")
	}
	conn, err := d.qmpSocket()
	if err != nil {
		return err
	}
	d.logf(fmt.Sprintf("  snapshot: writing the machine state to %s", path))

	// A LONG SNAPSHOT REPORTS PROGRESS. A 12 GiB state file is a minute of work, and a step that
	// printed nothing until it finished is indistinguishable from a hang.
	done := make(chan struct{})
	go func() {
		tick := time.NewTicker(15 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				if st, n, err := conn.MigrationStatus(); err == nil && st == "active" {
					d.logf(fmt.Sprintf("  snapshot: %s, %s written", st, humanBytes(n)))
				}
			}
		}
	}()
	err = conn.MigrateToFile(path, snapshotTimeout)
	close(done)
	if err != nil {
		return err
	}
	if fi, statErr := os.Stat(path); statErr == nil {
		d.logf(fmt.Sprintf("  snapshot: %s is %s", path, humanBytes(fi.Size())))
	}
	return nil
}

// snapshotTimeout is how long a migration may take.
//
// IT IS GENEROUS BECAUSE THE FILE IS THE GUEST'S MEMORY. 8 GiB of RAM took about 50 seconds on the
// node this was measured on, and a slower node with more memory would take minutes; a tight timeout
// would turn a slow node into a broken snapshot.
const snapshotTimeout = 20 * time.Minute

// humanBytes renders a size for a log line.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// QMP exposes the connection so the interactive surface can drive the machine.
//
// IT RETURNS THE DAEMON'S OWN CONNECTION RATHER THAN OPENING ONE, because qemu serves one client per
// socket: a second `qmp.Registry.Acquire` on the same path would block on the greeting and look like
// a hung guest. That is the same rule `Registry` exists to enforce, and it is why the click and key
// commands live in the daemon rather than in a process of their own.
func (d *Driver) QMP() (*qmp.Conn, error) { return d.qmpSocket() }

// ConsolePort is where the noVNC page is actually listening, or 0 when there is none.
//
// IT ANSWERED A LIE: `status` printed a URL for `req.NoVNCPort`, the number on the command line, so
// a daemon whose image lacked /usr/share/novnc reported `http://127.0.0.1:6080/vnc.html` for a port
// nothing was bound to. The console server is the only thing that knows, and this is how the daemon
// asks it.
func (d *Driver) ConsolePort() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.page == nil {
		return 0
	}
	return d.novncPort
}
