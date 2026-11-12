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
