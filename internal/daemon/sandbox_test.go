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
	// AND THE SUCCESS PATH HANDS THE SLOT TO `create`, so `destroy` has a closer and the machine
	// outlives the steps. THE OWNER IS THE RECORD: there used to be a `sandbox` flag and a
	// `sandboxClose` field beside `machine`/`machineClose`, and the four could disagree - which is
	// how `create` once built a second driver over a `run`'s machine.
	if !strings.Contains(code, "owner = ownerCreate") {
		t.Fatal("`bootSandbox` does not give the slot to `create`, so the slot still says it belongs " +
			"to a run - and a run closes its machine when its file ends")
	}
	// AND IT MUST NOT `defer` IT, which would close on return.
	if strings.Contains(code, "defer closeDriver()") {
		t.Fatal("`bootSandbox` defers closeDriver, which closes the machine as soon as the boot " +
			"returns - that is `run`, not `create`")
	}
	// AND THE SLOT IS CHECKED FIRST, so a second `create` is refused rather than starting a second
	// qemu. The check is in `startSandbox`; this asserts the owner is what carries the fact.
	if !strings.Contains(code, "d.machine != nil") {
		t.Fatal("`bootSandbox` does not check the slot before setting the owner")
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

	// THE SLOT IS ASKED FOR ITS PHASE, NOT RE-DERIVED FROM A FIELD.
	//
	// `d.machine == nil` USED TO BE THE TEST, and it cannot tell "no machine" from "reserved, no driver
	// yet" - so a `create`, which reserves the slot before this function runs, would have built a
	// SECOND driver over the first one's disk.
	if !strings.Contains(code, "d.slot()") {
		t.Fatal("`runWalkthroughFile` does not ask the slot for its phase, so it cannot tell a " +
			"reserved slot from an empty one - and a sandbox reserves before this runs")
	}
	// AND A FRESH DRIVER IS PUBLISHED INTO THE RESERVATION, which is what pairs it with its closer.
	if !strings.Contains(code, "d.publish(slot, fresh, closeFresh)") {
		t.Fatal("`runWalkthroughFile` does not publish the driver into the slot it reserved, so the " +
			"slot can end up with a driver nothing can stop")
	}
	// AND WHETHER IT CLOSES DEPENDS ON THE SLOT'S OWNER, NOT ON A FLAG BESIDE IT.
	if !strings.Contains(code, "slot.owner == ownerCreate") {
		t.Fatal("`runWalkthroughFile` does not consult the slot's owner, so either every bake would " +
			"leak a guest or every sandbox would close after its first file")
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

// A FILE WITH NO `boot:` STEP STILL HAS TO BE FOR THE RIGHT GUEST.
//
// `doBoot` REFUSES A MISMATCHED `os:` ON A RUNNING MACHINE, and that is not enough: a `dev.yaml` that
// only adds packages, or a `test.yaml` that only runs commands, has NO boot step - so it would never
// reach that check and would act on whatever guest happened to be running. A windows file typing into a
// Linux guest fails as a missing command twenty steps later, with nothing pointing at the real mistake.
//
// SO THE CHECK IS WHERE THE MACHINE IS REUSED, not only where it is started, and the file's guest is
// read from its boot steps. A file that names none is accepted: it is a set of actions, and the machine
// decides what it is.
func TestAReusedMachineIsTheGuestTheFileExpects(t *testing.T) {
	body, err := os.ReadFile("transport.go")
	if err != nil {
		t.Fatal(err)
	}
	src := stripGoComments(string(body))

	// THE CHECK IS AT REUSE, NOT ONLY AT BOOT.
	i := strings.Index(src, "case phase == slotRunning && slot != nil:")
	if i < 0 {
		t.Fatal("no reuse branch; the reuse path has moved")
	}
	window := src[i:]
	if end := strings.Index(window, "\n\t// THE HOOK IS HOW"); end > 0 {
		window = window[:end]
	}
	if !strings.Contains(window, "fileOS(file)") {
		t.Fatal("the reuse path does not check the file's guest, so a boot-less file would run " +
			"against whatever machine happened to be up")
	}
	// AND `fileOS` READS THE BOOT STEPS.
	j := strings.Index(src, "func fileOS(")
	if j < 0 {
		t.Fatal("no fileOS")
	}
	fn := src[j:]
	if end := strings.Index(fn[1:], "\nfunc "); end > 0 {
		fn = fn[:end+1]
	}
	fn = stripGoComments(fn)
	if !strings.Contains(fn, `s.Has("boot")`) || !strings.Contains(fn, `s.Get("os")`) {
		t.Fatal("`fileOS` does not read `os:` from the file's `boot:` steps")
	}
}
