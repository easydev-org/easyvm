package daemon

import (
	"fmt"
	"time"

	"github.com/easydev-org/easyvm/internal/run"
	"github.com/easydev-org/easyvm/internal/walk"
)

// --- the sandbox mode -------------------------------------------------------------------
//
// A BAKE AND A SANDBOX ARE DIFFERENT PRODUCTS, AND THE DIFFERENCE IS WHEN THE MACHINE STOPS.
//
//	`run`     the walkthrough runs to its last step and `defer closeDriver()` closes the machine.
//	          Every CI leg in this repository is this, and it is why that defer was never wrong
//	          there: a bake that outlived its file would hold /dev/kvm forever.
//
//	`create`  THE MACHINE STARTS AND STAYS. There is no last step to run out of. A sandbox pod is a
//	          guest a caller can ssh into, screenshot, and type at for as long as it wants, and
//	          `destroy` is the only ending it has.
//
// THE FILE IS STILL THE SHAPE. `create` loads the walkthrough and runs ONLY its `boot:` steps - os,
// disk, share, video adapter, the firmware - because that is where the machine's shape lives. It runs
// NO other step: a sandbox that executed a walkthrough would be a bake with extra words.
//
// WHY NOT `run` WITH A HUGE TIMEOUT: a bake still ends by running out of steps. The closing behaviour
// would then depend on a duration, and that is wrong on the day a slow install takes longer than the
// number. Two modes, two endings, no duration in the rule.

// startSandbox boots the machine a file describes and returns, leaving it running.
func (d *Daemon) startSandbox(req Request) Response {
	d.mu.Lock()
	if d.sandbox {
		st := d.st
		d.mu.Unlock()
		return Response{OK: false, Error: "a sandbox is already running; destroy it first",
			State: &st}
	}
	if d.ex != nil {
		st := d.st
		d.mu.Unlock()
		return Response{OK: false, Error: "a run is in progress; stop it first", State: &st}
	}
	if req.File == "" {
		d.mu.Unlock()
		return Response{OK: false, Error: "`create` needs a walkthrough file that describes " +
			"the machine - its `boot:` steps are the shape"}
	}
	work := d.work
	d.st = State{
		Status:    "starting",
		File:      req.File,
		Started:   time.Now(),
		NoVNCPort: req.NoVNCPort,
	}
	done := make(chan struct{})
	d.done = done
	d.mu.Unlock()

	go d.bootSandbox(req, work, done)

	return Response{OK: true, State: ptr(d.snapshot())}
}

// ptr is the address of a value, for the Response's optional State.
func ptr(s State) *State { return &s }

// bootSandbox loads the file, runs its boot steps, and KEEPS THE MACHINE.
func (d *Daemon) bootSandbox(req Request, work string, done chan struct{}) {
	// IT IS NOT CLOSED HERE, AND THAT IS THE WHOLE DIFFERENCE FROM runWalkthrough.
	//
	// `runWalkthrough` does `defer closeDriver()`; this one deliberately does not. `destroy` closes
	// it - and a daemon that exited without closing would leave qemu holding /dev/kvm with nothing
	// able to reach it, which is why SIGTERM still calls requestShutdown.

	fail := func(msg string, err error) {
		d.mu.Lock()
		d.st.Status = "failed"
		d.st.Message = msg
		if err != nil {
			d.st.Error = err.Error()
		}
		d.mu.Unlock()
	}

	overrides := map[string]string{
		"args.out":  req.Out,
		"args.work": work,
	}
	for k, v := range req.Vars {
		if v != "" {
			overrides["args."+k] = v
		}
	}
	file, err := walk.Load(req.File, overrides)
	if err != nil {
		fail("the walkthrough could not be loaded", err)
		return
	}

	driver, closeDriver, err := NewDriver(file, work, req.BootDisk, file.SSHPort,
		req.Password, req.NoVNCPort)
	if err != nil {
		fail("the machine could not be prepared", err)
		return
	}

	boots := bootSteps(file)
	if len(boots) == 0 {
		closeDriver()
		fail("nothing to boot", fmt.Errorf("`%s` has no `boot:` step, so `create` has no machine "+
			"to start. A sandbox file needs one; `run` needs steps", req.File))
		return
	}

	ex := &run.Executor{
		Driver:   driver,
		File:     file,
		Work:     work,
		SSHPort:  file.SSHPort,
		Password: req.Password,
		OnStep: func(index int, total int, name string) {
			d.mu.Lock()
			d.st.Step = name
			d.st.StepIndex = index
			d.st.StepCount = total
			d.mu.Unlock()
		},
		ShouldStop: func() bool {
			d.mu.Lock()
			defer d.mu.Unlock()
			return d.stopping
		},
	}

	// ONLY THE BOOT STEPS, IN ORDER, AND NO OTHERS.
	res, runErr := ex.Run(boots)
	if runErr != nil {
		closeDriver()
		fail("the machine could not be started", runErr)
		return
	}

	d.mu.Lock()
	d.sandbox = true
	d.sandboxClose = closeDriver
	d.ex = nil
	d.st.Status = "running"
	d.st.Answered = res.Answered
	d.st.Message = "the machine is up and stays up until `destroy`"
	if m := driver.Machine(); m != nil {
		if s, ok := m.(interface{ SSHAddress() string }); ok {
			d.st.SSH = s.SSHAddress()
		}
	}
	d.mu.Unlock()
}

// bootSteps is the `boot:` steps of a file, in order.
//
// A `boot:` WITH `os:` IS A BOOT, as the dispatcher already says. Anything else - a `disk:`, a
// `share_serve:` that the boot depends on, a `fetch:` of the ISO - is NOT run by `create`: those are
// things a walkthrough does on the way to a boot, and a sandbox file states its boot whole.
func bootSteps(f *walk.File) []walk.Step {
	var out []walk.Step
	for _, s := range f.Steps {
		if s.Has("boot") || s.Has("boot_from") {
			out = append(out, s)
		}
	}
	return out
}

// destroySandbox stops the machine a `create` started.
func (d *Daemon) destroySandbox() Response {
	d.mu.Lock()
	closeFn := d.sandboxClose
	d.sandboxClose = nil
	was := d.sandbox
	d.sandbox = false
	if closeFn == nil {
		d.mu.Unlock()
		if !was {
			return Response{OK: false, Error: "no sandbox is running"}
		}
		return Response{OK: true}
	}
	d.mu.Unlock()

	closeFn()

	d.mu.Lock()
	d.st.Status = "destroyed"
	d.st.Message = "the machine was stopped"
	st := d.st
	d.mu.Unlock()
	return Response{OK: true, State: &st}
}
