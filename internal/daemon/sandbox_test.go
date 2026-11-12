package daemon

import (
	"os"
	"strings"
	"testing"
)

// A BAKE ENDS BY RUNNING OUT OF STEPS; A SANDBOX HAS NO STEPS TO RUN OUT OF.
//
// THE DIFFERENCE IS ONE `defer`, AND IT IS THE WHOLE PRODUCT DIFFERENCE:
//
//	runWalkthrough   defer closeDriver()   the machine goes away with the file
//	bootSandbox      no defer             the machine stays until `destroy`
//
// A CI leg wants the first: a bake that outlived its file would hold /dev/kvm forever. A sandbox pod
// wants the second: a guest you can ssh into, screenshot and type at for as long as you want.
func TestTheSandboxDoesNotCloseItsDriver(t *testing.T) {
	body, err := os.ReadFile("sandbox.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	i := strings.Index(src, "func (d *Daemon) bootSandbox(")
	if i < 0 {
		t.Fatal("no bootSandbox")
	}
	rest := src[i:]
	if end := strings.Index(rest[1:], "\nfunc "); end > 0 {
		rest = rest[:end+1]
	}
	code := stripGoComments(rest)

	// THE FAILURE PATHS CLOSE IT, because a machine that could not start must not hold /dev/kvm.
	if !strings.Contains(code, "closeDriver()") {
		t.Fatal("`bootSandbox` never closes the driver, so a machine that FAILED to start would " +
			"hold /dev/kvm with nothing able to reach it")
	}
	// AND THE SUCCESS PATH STORES IT FOR `destroy` INSTEAD.
	if !strings.Contains(code, "d.sandboxClose = closeDriver") {
		t.Fatal("`bootSandbox` does not keep closeDriver, so `destroy` has nothing to call and the " +
			"machine outlives every command")
	}
	// AND IT MUST NOT `defer` IT, which would close on return.
	if strings.Contains(code, "defer closeDriver()") {
		t.Fatal("`bootSandbox` defers closeDriver, which closes the machine as soon as the boot " +
			"returns - that is `run`, not `create`")
	}
	// AND THE FLAG IS SET, so a second `create` is refused rather than starting a second qemu.
	if !strings.Contains(code, "d.sandbox = true") {
		t.Fatal("`bootSandbox` does not record that a sandbox is running")
	}
}

// A SANDBOX RUNS THE FILE'S `boot:` STEPS AND NO OTHERS.
//
// A sandbox that executed a walkthrough would be a bake with extra words. The file is still the
// shape - os, disk, share, video adapter - because that is where shape lives, and nothing else about
// it applies.
func TestASandboxRunsOnlyItsBootSteps(t *testing.T) {
	body, err := os.ReadFile("sandbox.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	i := strings.Index(src, "func bootSteps(")
	if i < 0 {
		t.Fatal("no bootSteps")
	}
	rest := src[i:]
	if end := strings.Index(rest[1:], "\nfunc "); end > 0 {
		rest = rest[:end+1]
	}
	code := stripGoComments(rest)
	if !strings.Contains(code, `s.Has("boot")`) {
		t.Fatal("`bootSteps` does not look for a `boot:` key")
	}
	if !strings.Contains(code, `s.Has("boot_from")`) {
		t.Fatal("`bootSteps` ignores `boot_from:`, so a sandbox that resumes from a snapshot would " +
			"have no machine to start")
	}
	// AND IT RETURNS ONLY THOSE, so nothing else can slip in.
	if !strings.Contains(code, "return out") {
		t.Fatal("`bootSteps` does not return the list it built")
	}
}

// AND THE CLI HAS BOTH ENDINGS.
func TestTheCLIOffersCreateAndDestroy(t *testing.T) {
	body, err := os.ReadFile("../../cmd/easyvm/main.go")
	if err != nil {
		t.Skipf("no main.go here (%v)", err)
	}
	src := string(body)
	for _, want := range []string{`case "create":`, `case "destroy":`,
		"func cmdCreate(", "func cmdDestroy("} {
		if !strings.Contains(src, want) {
			t.Fatalf("the CLI has no %q, so a sandbox cannot be started or ended from a shell",
				want)
		}
	}
	// AND `create` WAITS FOR `running` RATHER THAN RETURNING WHEN THE REQUEST DOES.
	if !strings.Contains(src, `case "running":`) {
		t.Fatal("`create` does not wait for the machine to be up, so it would print success for a " +
			"boot that has not happened yet")
	}
}

// A YAML IS A SET OF ACTIONS ON A MACHINE, AND THE MACHINE OUTLIVES EVERY FILE.
//
// THE FIRST MODEL WAS ONE FILE PER MACHINE: `runWalkthrough` called `NewDriver` and deferred its
// closer, so each YAML got a fresh qemu. That is a bake, and it is right for a CI leg.
//
// A SANDBOX IS THE OTHER WAY ROUND - `init.yaml` installs, `dev.yaml` adds a toolchain, `test.yaml`
// runs the tests, ALL ON ONE MACHINE BOOTED ONCE. So the driver lives in `d.machine`, created on the
// first file that needs one and closed by `destroy` (or by a bake's own ending).
//
// AND A `boot:` STEP AGAINST A RUNNING MACHINE IS A NO-OP, not an error: a later file says "this
// machine must be running", and it is. The same rule as `mkdir -p`.
func TestAMachineOutlivesTheFileThatBootedIt(t *testing.T) {
	body, err := os.ReadFile("transport.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	i := strings.Index(src, "func (d *Daemon) runWalkthroughFile(")
	if i < 0 {
		t.Fatal("no runWalkthroughFile - the file is not split from the driving, so a sandbox " +
			"cannot hand over a narrowed file without a second copy of the machinery")
	}
	rest := src[i:]
	if end := strings.Index(rest[1:], "\nfunc "); end > 0 {
		rest = rest[:end+1]
	}
	code := stripGoComments(rest)

	// THE DRIVER IS REUSED, NOT RECREATED PER FILE.
	if !strings.Contains(code, "driver := d.machine") {
		t.Fatal("`runWalkthroughFile` does not look for a machine the daemon already holds, so " +
			"every YAML would boot its own qemu")
	}
	// AND A FRESH ONE IS STORED FOR THE NEXT FILE.
	if !strings.Contains(code, "d.machine = fresh") {
		t.Fatal("`runWalkthroughFile` does not keep the driver it created, so the next file would " +
			"boot a second machine")
	}
	// AND WHETHER IT CLOSES DEPENDS ON THE MODE, NOT ON THE FILE.
	if !strings.Contains(code, "keep := d.sandbox") {
		t.Fatal("`runWalkthroughFile` does not consult `d.sandbox`, so either every bake would leak " +
			"a guest or every sandbox would close after its first file")
	}
}

// AND THE BOOT STEP DOES NOT REFUSE A RUNNING MACHINE.
func TestBootIsANoOpOnARunningMachine(t *testing.T) {
	body, err := os.ReadFile("../run/actions.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	i := strings.Index(src, "func (e *Executor) doBoot(")
	if i < 0 {
		t.Fatal("no doBoot")
	}
	rest := src[i:]
	if end := strings.Index(rest[1:], "\nfunc "); end > 0 {
		rest = rest[:end+1]
	}
	code := stripGoComments(rest)
	if !strings.Contains(code, "IsRunning()") {
		t.Fatal("`doBoot` does not ask whether the machine is already up, so a second YAML's boot " +
			"step would fail with \"a machine is already running\" - which is a bake's rule, not a " +
			"sandbox's")
	}
	if !strings.Contains(code, "already running:") {
		t.Fatal("`doBoot` does not log the no-op, so a reader cannot tell a skipped boot from an " +
			"unnoticed one")
	}
}
