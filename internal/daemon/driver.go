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
	page     *consoleServer
	vncPort  int
	netUp    bool
	logf     func(string)
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
func (m machineControl) Disk() string {
	m.d.mu.Lock()
	defer m.d.mu.Unlock()
	return m.d.diskPath
}

// Start launches qemu with the ONE command line every family shares.
func (m machineControl) Start() error {
	d := m.d
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.proc != nil {
		return fmt.Errorf("a machine is already running")
	}
	if d.diskPath == "" {
		return fmt.Errorf("no disk to boot; a `disk:` step must come first, or pass --boot-disk")
	}

	opts, err := machine.CommonOptions{SSHPort: d.port}.Normalise()
	if err != nil {
		return err
	}
	cpu, err := machine.ResolveCPU("")
	if err != nil {
		return err
	}

	qmpPath := filepath.Join(d.work, "drive.sock.qmp")
	_ = os.Remove(qmpPath)

	// THE DISPLAY NUMBER IS FIXED AT 0 INSIDE THE CONTAINER: one guest per container, and a
	// computed number would make the log line and the -vnc argument disagree about it.
	const vncDisplay = 0
	d.vncPort = vncBase + vncDisplay

	args := []string{
		"-machine", "q35",
		// THE CPU IS FIXED FOR EVERY GUEST. See internal/machine: `host` makes a run
		// unreproducible across runners, and cachyos panicked on the first second of boot because
		// of it.
		"-cpu", cpu,
		"-smp", fmt.Sprint(opts.CPUs),
		"-m", fmt.Sprint(opts.MemMB),
		"-vga", opts.VGA,
		"-display", "none",
		"-drive", "file=" + d.diskPath + ",if=virtio,format=qcow2",
		"-qmp", "unix:" + qmpPath + ",server=on,wait=off",
		// VNC LISTENS ON THE LOOPBACK ONLY, and websockify is what a browser reaches. Binding
		// qemu's own VNC to 0.0.0.0 would expose an unauthenticated VNC server; websockify in
		// front of it on 127.0.0.1 is the same page with one door.
		"-vnc", fmt.Sprintf("127.0.0.1:%d", vncDisplay),
		// THE ONE NETWORK MODE: tap on a bridge that internal/net built. There is no `-netdev user`
		// anywhere in this program, and the reason is in that package's header - the host's smbd
		// listens on the bridge address, and a user-mode guest cannot route to it.
		//
		// `script=no,downscript=no` BECAUSE THERE IS NO HELPER. qemu's default runs /etc/qemu-ifup
		// when the tap comes up, and a container has no such file - so the default fails loudly,
		// after the guest has been created.
		"-netdev", fmt.Sprintf("tap,id=net0,ifname=%s,script=no,downscript=no",
			easyvmnet.TapName),
		// `host_mtu` IS THE ONLY WAY THE GUEST LEARNS THE LINK'S MTU. A guest at 1500 over a 1450
		// tap drops every full segment, and the symptom is a TLS handshake that never completes
		// rather than a network error. `romfile=` must be empty: a value there is read as a
		// firmware path.
		"-device", fmt.Sprintf("virtio-net-pci,netdev=net0,mac=%s,romfile=,host_mtu=%d",
			easyvmnet.TapMAC, easyvmnet.MTU),
		"-device", "virtio-tablet-pci",
	}

	// FIRMWARE IS THE ONE THING THAT IS GENUINELY PER-FAMILY. Windows and Linux take OVMF; macOS
	// takes OpenCore. It is a fact about the guest, not an accident of who wrote the file.
	ovmfCode := os.Getenv("OVMF_CODE")
	if ovmfCode == "" {
		ovmfCode = "/usr/share/OVMF/OVMF_CODE_4M.fd"
	}
	ovmfVars := filepath.Join(d.work, "OVMF_VARS.fd")
	tmpl := os.Getenv("OVMF_VARS_TEMPLATE")
	if tmpl == "" {
		tmpl = "/usr/share/OVMF/OVMF_VARS_4M.fd"
	}
	if _, err := os.Stat(ovmfVars); err != nil {
		if err := copyFile(tmpl, ovmfVars); err != nil {
			return fmt.Errorf("firmware variables: %w", err)
		}
	}
	args = append(args,
		"-drive", "if=pflash,format=raw,readonly=on,file="+ovmfCode,
		"-drive", "if=pflash,format=raw,file="+ovmfVars,
	)

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

	// THE PAGE, ONCE THE MACHINE IS UP. It is a child of this driver, so Stop() and the shutdown
	// path reap it - no supervisor, and no detached process outliving the run.
	if d.novncPort > 0 {
		page, err := startConsole("127.0.0.1", d.novncPort, d.vncPort, d.logf)
		if err != nil {
			// A PORT ALREADY IN USE IS A CONFIGURATION MISTAKE, not a missing package, so it
			// stops the boot rather than continuing without a page.
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

func (m machineControl) Reboot() error {
	if err := m.Stop("kill"); err != nil {
		return err
	}
	time.Sleep(2 * time.Second)
	return m.Start()
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
