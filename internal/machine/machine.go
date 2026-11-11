// Package machine owns the qemu command line, and the argument set is deliberately the SAME
// for every guest family.
//
// # WHY UNIFORMITY IS THE POINT
//
// The Python implementation built a different command line per family, and the differences were
// never intentional: macOS needed smc and OSK, Windows needed a TPM, Linux needed neither - and
// each of those accumulated its own quirks until the three were not comparable. A guest that
// failed on one platform could not be re-run on another to ask whether the fault was the disk or
// the command line, because the command line was not the same thing.
//
// EVERY GUEST GETS THE SAME CPU MODEL. That is not a preference, it is a requirement:
//
//	-cpu host     exposes the runner's actual microarchitecture. A disk whose packages are built
//	              for a specific level then behaves differently on every machine - cachyos'
//	              packages are built for x86-64-v3 and the guest panicked in the first second on
//	              a runner whose host CPU advertised something else. The failure looked like a
//	              broken disk and was a broken machine definition.
//
//	-cpu Skylake-Client-v4   is a fixed, named microarchitecture level. v4 is x86-64-v4 (AVX-512),
//	              which is the highest level the model exposes while remaining a model qemu
//	              emulates rather than a passthrough. It makes every guest identical regardless of
//	              which runner picked the job up, so a stage that passes on one runner passes on
//	              the next.
//
// `host` IS REFUSED BY NAME rather than merely defaulted away, because "it worked on my runner"
// is exactly the kind of difference that costs a day of CI.
package machine

import "fmt"

// CPUModel is the one CPU every guest is started with.
//
// IT IS NOT CONFIGURABLE. A file may not override it and no flag may replace it: the whole value
// of naming it is that a run is reproducible on a different machine, and an option to change it
// would be an option to make that untrue.
const CPUModel = "Skylake-Client-v4"

// ErrHostCPU is returned whenever a caller asks for -cpu host.
var ErrHostCPU = fmt.Errorf(
	"-cpu host is not allowed: every guest starts with -cpu %s so a run is reproducible on a "+
		"different runner. See the note in internal/machine/machine.go on why the Python "+
		"implementation's -cpu host broke cachyos", CPUModel)

// ResolveCPU validates a requested CPU model and returns the one to use.
//
// An empty request means "the standard model", which is the normal case. Anything else is checked
// against CPUModel, so the only two accepted answers are "" and CPUModel itself.
func ResolveCPU(requested string) (string, error) {
	switch requested {
	case "", CPUModel:
		return CPUModel, nil
	case "host":
		return "", ErrHostCPU
	}
	return "", fmt.Errorf("cpu %q is not allowed: every guest starts with -cpu %s, so a run is "+
		"reproducible on a different runner", requested, CPUModel)
}

// CommonOptions is the part of the qemu command line that is identical for every family.
//
// # WHAT IS DELIBERATELY OUTSIDE IT
//
// Three things genuinely differ, and pretending otherwise would be worse than the asymmetry:
//
//	machine   q35 for Windows and Linux, q35 for macOS too but with smc and OSK appended
//	firmware  OVMF for Windows/Linux, OpenCore for macOS
//	devices   a TPM for Windows, none for macOS (it answers the power button with a dialog)
//
// Those are listed once, per family, in the caller - and every one of them is a fact about the
// guest, not an accident of who wrote the file.
type CommonOptions struct {
	CPUs    int
	MemMB   int
	VGA     string
	SSHPort int
}

// Defaults are the numbers every stage uses when it does not say otherwise.
const (
	DefaultCPUs    = 4
	DefaultMemMB   = 4096
	DefaultVGA     = "std"
	DefaultSSHPort = 2222
)

// Normalise fills in the defaults and refuses values that would make a guest unrunnable.
func (o CommonOptions) Normalise() (CommonOptions, error) {
	if o.CPUs <= 0 {
		o.CPUs = DefaultCPUs
	}
	if o.MemMB <= 0 {
		o.MemMB = DefaultMemMB
	}
	if o.VGA == "" {
		o.VGA = DefaultVGA
	}
	if o.SSHPort <= 0 {
		o.SSHPort = DefaultSSHPort
	}
	if o.CPUs > 64 {
		return o, fmt.Errorf("%d cpus is not a machine anyone here runs; the limit is a guard "+
			"against a misparsed value", o.CPUs)
	}
	if o.MemMB < 512 {
		return o, fmt.Errorf("%d MiB is below the smallest guest this repository starts", o.MemMB)
	}
	return o, nil
}
