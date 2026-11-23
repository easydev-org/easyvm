package daemon

import (
	"os"
	"strings"
	"testing"

	"github.com/easydev-org/easyvm/internal/machine"
)

// THERE IS ONE NETWORK MODE AND IT IS THE TAP.
//
// qemu can also do `-netdev user`, which needs no /dev/net/tun, no NET_ADMIN and no bridge. It is
// not available in this program, and the reason is a fact about the guest rather than a preference:
//
//	the host's smbd listens on 172.30.0.1, and a user-mode guest lives on 10.0.2.0/24 and cannot
//	route to it. Every `share_mount:` step would fail with a network error that reads as a broken
//	share.
//
// THE RULE IS ASSERTED AGAINST THE COMMAND LINE, not against a flag: a flag can be defaulted wrong,
// and the failure it produces is a stage that runs for twenty minutes and then cannot reach its
// guest. Reading the source is the only way to catch a `-netdev user` that someone adds back.
func TestThereIsNoUserModeNetworking(t *testing.T) {
	body := readSource(t, "driver.go")

	// STRIP COMMENTS FIRST. The file EXPLAINS `-netdev user` at length - why it is not used - and a
	// grep over the whole file finds it in the explanation. That mistake was made twice already in
	// this repository's checks.
	code := stripComments(body)

	if strings.Contains(code, "user,id=") || strings.Contains(code, `"-netdev", "user`) {
		t.Fatal("the command line contains a user-mode netdev. The host's smbd listens on the " +
			"bridge address and a user-mode guest cannot reach it, so every share_mount: step " +
			"would fail with an error that reads as a broken share")
	}
	// THE NETDEV IS IN THE SHAPES NOW, so the assertion reads them. Every profile of every shape a
	// stage can boot must put the guest on the bridge: the host's smbd listens on the bridge address
	// and a user-mode guest cannot reach it.
	joined := strings.Join(shapeArgs(t), " ")
	if !strings.Contains(joined, "tap,id=") {
		t.Fatal("no shape puts a guest on a tap; the guest must be on the bridge")
	}
	if !strings.Contains(joined, "script=no") || !strings.Contains(joined, "downscript=no") {
		t.Fatal("a shape's netdev does not say script=no,downscript=no: qemu would otherwise run " +
			"/etc/qemu-ifup, which does not exist in a container")
	}
	// AND THE MTU FEATURE AND AN EMPTY `romfile=` ARE THE SHAPE'S, for the same reason.
	//
	// `host_mtu=` IS NOT DECORATION: without it the guest keeps 1500 over a 1450-byte link and every
	// full segment is dropped, and the symptom is a TLS handshake that never completes.
	//
	// `romfile=` MUST BE PRESENT AND EMPTY: it suppresses the iPXE option ROM, and a non-empty value
	// is read as a firmware image path.
	if !strings.Contains(joined, "host_mtu=") {
		t.Fatal("a shape's virtio-net does not carry host_mtu=")
	}
	if !strings.Contains(joined, "romfile=,") {
		t.Fatal("a shape's virtio-net does not carry an empty `romfile=`")
	}
}

// THE NETWORK IS BROUGHT UP BEFORE QEMU, because qemu opens the tap itself and a tap that does not
// exist is reported as a qemu failure rather than a network one.
func TestTheNetworkStartsBeforeTheMachine(t *testing.T) {
	code := stripComments(readSource(t, "driver.go"))
	up := strings.Index(code, "easyvmnet.Up(")
	start := strings.Index(code, "cmd.Start()")
	if up < 0 {
		t.Fatal("internal/net's Up is never called; the tap would not exist")
	}
	if start < 0 {
		t.Fatal("no cmd.Start() found; the test is looking for the wrong shape")
	}
	if up > start {
		t.Fatal("the network is brought up AFTER qemu starts; qemu would fail with " +
			"`could not configure /dev/net/tun`, which reads as a qemu problem")
	}
	// AND IT IS TORN DOWN, or the bridge and the NAT rules accumulate one per run.
	if !strings.Contains(code, "easyvmnet.Down()") {
		t.Fatal("the network is never torn down; each run would leave a bridge, a tap and a set " +
			"of iptables rules behind")
	}
}

func readSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(b)
}

// stripComments drops whole-line // comments, which is where every explanation in these files
// lives. Prose that names a forbidden argument must not satisfy a check for it - that mistake was
// made twice already in this repository's assertions.
func stripComments(src string) string {
	var out []string
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// shapeArgs is every argument pair of every profile the shapes this checkout has.
func shapeArgs(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, name := range machine.Shapes().Names() {
		sh, _ := machine.Shapes().Lookup(name)
		for _, prof := range []string{"install", "disk", "q35", "serial", "terminal", "desktop", ""} {
			pr, err := sh.Profile(prof)
			if err != nil {
				continue
			}
			for _, pair := range pr.Args {
				out = append(out, strings.Join(pair, " "))
			}
		}
	}
	if len(out) == 0 {
		t.Skip("no shapes here, so there is no command line to read")
	}
	return out
}
