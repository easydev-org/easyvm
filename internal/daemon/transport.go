package daemon

import (
	"fmt"
	"path/filepath"

	"github.com/easydev-org/easyvm/internal/run"
	"github.com/easydev-org/easyvm/internal/walk"
)

// THERE IS NO UNIX-SOCKET TRANSPORT ANY MORE. It lived here as `listenUnix`, `socketAnswers`,
// `acceptLoop` and `serveConn`, and the last of those had the wrong shape:
//
//	for {
//		conn, _ := ln.Accept()
//		d.serveConn(conn)     // reads until the CLIENT closes
//		conn.Close()
//	}
//
// A poller that kept its connection open held the entire loop, so nothing else was accepted - and the
// HTTP surface then blocked behind the machine's lock:
//
//	goroutine 1   [IO wait]                     <- acceptLoop, inside serveConn
//	goroutine 66  [sync.Mutex.Lock] 3 minutes   <- http / -> vncPort -> d.mu
//
// which is a daemon that accepts `curl` and never answers, on a transport nobody needed. HTTP is the
// only one now, and `net/http` starts a goroutine per connection itself. See `Serve`.

// THE EXECUTOR'S DRIVER IS THE ONE THIS PACKAGE OWNS, so the machine and its position in the
// walkthrough live in the same process. That is the structural claim the package exists to make.
func (d *Daemon) runWalkthrough(req Request, work string, done chan struct{}) {
	defer close(done)

	fail := func(msg string, err error) {
		d.mu.Lock()
		d.st.Status = "failed"
		d.st.Message = msg
		if err != nil {
			d.st.Error = err.Error()
		}
		d.ex = nil
		d.mu.Unlock()
	}

	// `args.work` IS A RUN FACT LIKE `args.out`. A `fetch:` puts its file in the work directory and
	// the `scp:` that follows names it - so the file has to be able to say where that is, rather
	// than hard-coding a path the daemon chose.
	overrides := map[string]string{
		"args.out":  req.Out,
		"args.work": work,
	}
	// THE CALLER'S VARIABLES. A command-line `--shutdown kill` reaches a file's `shutdown:` var the
	// same way `--out` reaches `{{ out }}`, so an empty one is dropped rather than turned into an
	// empty string - "present but empty" and "absent, use the default" are different answers and
	// the file has a default for exactly this reason.
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
	d.runWalkthroughFile(req, work, done, file)
}

// runWalkthroughFile is the half that drives an ALREADY-LOADED file, so a sandbox can narrow one to
// its boot steps and hand it over rather than keeping a second copy of this machinery.
func (d *Daemon) runWalkthroughFile(req Request, work string, done chan struct{}, file *walk.File) {
	fail := func(msg string, err error) {
		d.mu.Lock()
		d.st.Status = "failed"
		d.st.Message = msg
		if err != nil {
			d.st.Error = err.Error()
		}
		d.ex = nil
		d.mu.Unlock()
	}

	// THE MACHINE IS THE LONG-LIVED THING; A YAML IS A SET OF ACTIONS ON IT.
	//
	// THIS USED TO BE `driver, closeDriver, err := NewDriver(...)` FOLLOWED BY `defer closeDriver()`:
	// every walkthrough got a fresh driver and therefore a fresh qemu, and the machine died with the
	// file. That is a bake, and it is right for one.
	//
	// A SANDBOX RUNS SEVERAL FILES AGAINST ONE MACHINE - `init.yaml` installs, `dev.yaml` adds a
	// toolchain, `test.yaml` runs the tests - so the driver is created on the FIRST file that needs
	// one and kept in `d.machine` until `destroy`. A later file finds it and reuses it; its `boot:`
	// step is a no-op because the machine is already up.
	//
	// AND A FILE THAT CHANGES THE DISK IS REFUSED, because the machine has already read it: a second
	// `disk:` on a running guest would be a file describing a machine that does not exist.
	d.mu.Lock()
	driver := d.machine
	d.mu.Unlock()

	if driver == nil {
		fresh, closeFresh, err := NewDriver(file, work, req.BootDisk, file.SSHPort,
			req.Password)
		if err != nil {
			fail("the machine could not be prepared", err)
			return
		}
		driver = fresh
		d.mu.Lock()
		d.machine = fresh
		d.machineClose = closeFresh
		d.mu.Unlock()
	} else {
		// THE GUEST HAS TO BE THE ONE THIS FILE EXPECTS, AND THAT CHECK IS HERE RATHER THAN IN `doBoot`.
		//
		// `doBoot` DOES refuse a mismatched `os:` on a running machine, and that is not enough: a file
		// with NO `boot:` step - a `dev.yaml` that only adds packages, a `test.yaml` that only runs
		// commands - would never reach that check, and it would act on whatever guest happened to be
		// running. A `windows` file typing into a Linux guest fails as a missing command twenty steps
		// later, with nothing pointing at the real mistake.
		//
		// THE FILE'S OWN `os:` IS READ FROM ITS BOOT STEPS, because that is where a file states its
		// guest. A file that names none is accepted: it is a set of actions and the machine decides
		// what it is.
		if want := fileOS(file); want != "" {
			if named, ok := driver.Machine().(interface{ OS() string }); ok {
				if have := named.OS(); have != "" && have != want {
					fail(fmt.Sprintf("this daemon already holds a %s machine, and this file is for "+
						"%s. One daemon holds one machine; `destroy` it to change the guest", have, want),
						nil)
					return
				}
			}
		}
		fmt.Printf("[easyvm]   reusing the machine already running for %s\n", filepath.Base(req.File))
	}

	// THE HOOK IS HOW `stop` REACHES A RUN THAT IS MID-STEP. Without it, `stop` could only set a
	// flag the executor never reads, and a run blocked in a 900-second ssh wait would ignore it.
	ex := &run.Executor{
		Driver:   driver,
		File:     file,
		Work:     work,
		SSHPort:  file.SSHPort,
		Password: req.Password,
		OnStep: func(index int, total int, name string) {
			d.mu.Lock()
			d.st.Status = "running"
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

	d.mu.Lock()
	d.ex = ex
	d.st.Status = "running"
	d.st.StepCount = len(file.Steps)
	d.mu.Unlock()

	res, runErr := ex.Run(file.Steps)

	// A RUN THAT FINISHED CLOSES ITS MACHINE; A RUN THAT FAILED LEAVES IT UP.
	//
	// THE VERDICT IS THE EXIT CODE AND NOTHING ELSE. A failed run still reports `failed`, still
	// returns the error and still exits non-zero, so a CI leg fails exactly as it did before. What
	// changed is only whether qemu is still there afterwards.
	//
	// WHY THAT IS THE RIGHT DEFAULT: A FAILED STEP IS THE ONE MOMENT A MACHINE IS WORTH KEEPING. The
	// walk has just stopped at the step that did not work, the screen is in the state that proves why,
	// and the first thing anyone does is look at it - over the browser console, over ssh, or by
	// pressing the button again by hand. Closing it throws away the only copy of the evidence and
	// costs a full re-run to get it back:
	//
	//	status: failed step 10/25 "welcome"
	//	error:  welcome: did not advance: wanted "keyboard", read "... Please select your language"
	//
	// That is a screenshot away from being diagnosed, and the screenshot no longer existed.
	//
	// A SANDBOX KEEPS ITS MACHINE FOR A DIFFERENT REASON - it has no end except `destroy` - and both
	// arrive at the same place, so one flag covers them.
	d.mu.Lock()
	keep := d.sandbox || runErr != nil
	closeFn := d.machineClose
	if !keep {
		d.machine = nil
		d.machineClose = nil
	} else {
		d.machineClose = nil
	}
	d.ex = nil
	d.st.Answered = res.Answered
	if runErr != nil {
		if d.stopping {
			d.st.Status = "finished"
			d.st.Message = "stopped as asked"
		} else {
			d.st.Status = "failed"
			d.st.Error = runErr.Error()
		}
	} else {
		d.st.Status = "finished"
		d.st.Message = fmt.Sprintf("%d step(s) ran", len(res.Answered))
	}
	d.mu.Unlock()

	if !keep && closeFn != nil {
		closeFn()
		return
	}
	// A KEPT MACHINE IS SAID OUT LOUD. The next `run` reuses it - `doBoot` treats a boot on a running
	// machine as a no-op - and a reader who was not told would think the second run never restarted
	// anything, or that the daemon was confused.
	if runErr != nil {
		fmt.Printf("[easyvm]   the machine is still up after the failure; `easyvm destroy` closes it\n")
	}
}

// SocketDir is where a daemon for a walkthrough file would put its socket, for a client that was
// given only the file.
func SocketDir(file, work string) string {
	if work != "" {
		return work
	}
	return filepath.Dir(file)
}

// fileOS is the guest a file says it needs, read from its `boot:` steps.
//
// A FILE THAT NAMES NONE RETURNS "", which means "any machine will do" - a set of actions rather than
// a description of a guest. That is how `dev.yaml` and `test.yaml` are written.
func fileOS(f *walk.File) string {
	for _, s := range f.Steps {
		if s.Has("boot") || s.Has("boot_from") {
			if os := s.Get("os"); os != "" {
				return os
			}
			if fam := s.Get("family"); fam != "" {
				return fam
			}
		}
	}
	return ""
}
