package machine

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// KVMGetAPIVersion is the ioctl that asks the KVM device what API it speaks.
//
// IT IS THE ONLY HONEST TEST OF "IS KVM USABLE" - opening the device is not enough, because a
// container can have the node and a cgroup that refuses the ioctl (or a device whose group the process
// is not in, which root sometimes dodges and sometimes does not).
const kvmGetAPIVersion = 0xAE00

// CheckKVM reports whether this machine can actually accelerate a guest, and says what is wrong when
// it cannot.
//
// THE FAILURE IT PREVENTS IS SILENT AND EXPENSIVE. `-machine q35` with no `-accel` lets qemu choose,
// and qemu's default is `tcg` - pure software emulation. The guest still boots. Measured on run
// 43063744565's macos-setup leg, whose log said
//
//	qemu-system-x86_64: warning: TCG doesn't support requested feature:
//	  CPUID[80000007h].EDX.invtsc [bit 8]
//
// and `+invtsc` is one of the twenty flags macOS needs - the flag whose absence deadlocks the guest at
// `[EB|LOG:HANDOFF TO XNU]` with every vCPU at 100%. On TCG that deadlock is not a risk, it is the
// expected outcome, and it arrives forty minutes in.
//
// SO THE ACCELERATOR IS NAMED IN THE COMMAND LINE AND THIS IS THE CHECK BEHIND IT: `-accel kvm` makes
// qemu refuse to start, and this makes the refusal a sentence about /dev/kvm rather than about a
// machine that will not come up.
func CheckKVM() error {
	const path = "/dev/kvm"
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s does not exist. This build starts every guest with `-accel kvm` and "+
				"has no software-emulation fallback: on TCG a macOS guest cannot get the `+invtsc` "+
				"flag it needs and deadlocks at HANDOFF TO XNU, and every other guest is fifty times "+
				"slower. Run the container with `--device /dev/kvm`", path)
		}
		return fmt.Errorf("%s exists but cannot be opened: %w. The node is there and this process "+
			"may not use it - a container needs `--device /dev/kvm`, and the device's group must be "+
			"one this process is in", path, err)
	}
	defer f.Close()

	// THE IOCTL IS THE REAL QUESTION. A device that opens and refuses its ioctl is a container whose
	// cgroup allows the node and not the operation.
	var version uint32
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), kvmGetAPIVersion,
		uintptr(unsafe.Pointer(&version)))
	if errno != 0 {
		return fmt.Errorf("%s opened but KVM_GET_API_VERSION failed: %w. Something opened the node "+
			"but the kernel module is not answering - nested virtualisation disabled on the host is "+
			"the usual cause", path, errno)
	}
	if version != 12 {
		// 12 IS THE ONLY API VERSION THE KERNEL HAS EVER SHIPPED, and a different number means this
		// is not the KVM this build was written against.
		return fmt.Errorf("%s reports KVM API version %d, and this build expects 12", path, version)
	}
	return nil
}
