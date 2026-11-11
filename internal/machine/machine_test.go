package machine

import (
	"errors"
	"strings"
	"testing"
)

// THE CPU MODEL IS THE ONE THING HERE THAT IS NOT A PREFERENCE.
//
// A runner whose host CPU differs from the last one produces a different guest, and the failure
// that costs is indistinguishable from a broken disk: cachyos' packages are built for x86-64-v3
// and the guest panicked in the first second under `-cpu host` on a runner that advertised
// something else. So the model is fixed and `host` is refused by name rather than defaulted away.
func TestHostCPUIsRefused(t *testing.T) {
	_, err := ResolveCPU("host")
	if err == nil {
		t.Fatal("-cpu host must be refused; it makes a run unreproducible across runners")
	}
	if !errors.Is(err, ErrHostCPU) {
		t.Fatalf("expected ErrHostCPU, got %v", err)
	}
	// The message has to say what to do instead, because it will be read in a CI log at the
	// moment somebody is trying to pass a flag.
	if !strings.Contains(err.Error(), CPUModel) {
		t.Fatalf("the refusal must name the model to use; got %q", err.Error())
	}
}

// ANY OTHER MODEL IS REFUSED TOO. An option to change the CPU is an option to make the
// reproducibility claim untrue, so the accepted set is exactly {"", CPUModel}.
func TestOnlyTheStandardModelIsAccepted(t *testing.T) {
	for _, requested := range []string{"", CPUModel} {
		got, err := ResolveCPU(requested)
		if err != nil {
			t.Fatalf("ResolveCPU(%q) should be accepted: %v", requested, err)
		}
		if got != CPUModel {
			t.Fatalf("ResolveCPU(%q) = %q, want %q", requested, got, CPUModel)
		}
	}
	for _, requested := range []string{"host", "max", "qemu64", "Skylake-Client-v3",
		"skylake-client-v4", "Skylake-Client-V4"} {
		if _, err := ResolveCPU(requested); err == nil {
			t.Fatalf("ResolveCPU(%q) must be refused", requested)
		}
	}
}

// THE MODEL NAME IS CASE-SENSITIVE IN QEMU and a near-miss would be accepted by a lenient check
// and then rejected by qemu at startup, which reads as a machine that will not boot.
func TestTheModelNameIsExact(t *testing.T) {
	if CPUModel != "Skylake-Client-v4" {
		t.Fatalf("CPUModel changed to %q; every measurement in this repository was taken with "+
			"Skylake-Client-v4, and a change invalidates them", CPUModel)
	}
}

func TestOptionsAreNormalised(t *testing.T) {
	got, err := CommonOptions{}.Normalise()
	if err != nil {
		t.Fatal(err)
	}
	if got.CPUs != DefaultCPUs || got.MemMB != DefaultMemMB ||
		got.VGA != DefaultVGA || got.SSHPort != DefaultSSHPort {
		t.Fatalf("defaults not applied: %+v", got)
	}
}

// A MISPARSE MUST NOT BECOME A GUEST WITH A MILLION MEGABYTES. The guard exists because these
// values arrive as strings from YAML, and an empty string that parses to 0 already produced two
// different timeouts from one blank in the Python implementation.
func TestAbsurdValuesAreRefused(t *testing.T) {
	if _, err := (CommonOptions{CPUs: 4096}).Normalise(); err == nil {
		t.Fatal("4096 cpus must be refused")
	}
	if _, err := (CommonOptions{MemMB: 8}).Normalise(); err == nil {
		t.Fatal("8 MiB must be refused")
	}
}
