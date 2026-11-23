package machine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE ADAPTER IS A PLATFORM FACT AND IT IS THE ONE THAT DECIDES WHETHER ANDROID DRAWS AT ALL.
//
// AND IT IS NOW WRITTEN IN A SHAPE RATHER THAN ANSWERED BY A FUNCTION. `Boot.VGA` used to return
// `virtio` for android, `none` for macOS and the default otherwise; the adapter is a line in the
// shape's `qemu.args` now, so this reads the files.
//
// HWC=drm_minigbm OPENS /dev/dri/card0, and a std VGA has no DRM device: the guest draws nothing and
// the screen stops on initrd's last line. That is why the adapter is not a preference.
func TestAndroidGetsVirtioVGA(t *testing.T) {
	android := profileArgs(t, "android")
	if !strings.Contains(android, "virtio-vga,xres=1920,yres=1080") {
		t.Errorf("android's qemu.args do not name `virtio-vga`, so HWC finds no /dev/dri/card0 and "+
			"the guest draws nothing:\n%s", android)
	}
	for _, name := range []string{"linux", "windows", "bsd"} {
		args := profileArgs(t, name)
		if !strings.Contains(args, "VGA,xres=1920,yres=1080,vgamem_mb=32") {
			t.Errorf("%s's qemu.args do not name the `-device VGA` form, so its guest comes up at "+
				"qemu's own 1280x800 and every measured coordinate is on a different screen:\n%s",
				name, args)
		}
	}
	// MACOS SUPPLIES ITS OWN ADAPTER, as a device in its `qemu.args`. A second one would give the
	// guest two displays, and `see:` might photograph the blank one.
	if mac := profileArgs(t, "macos"); strings.Contains(mac, "VGA,xres=") {
		t.Errorf("macOS is given a VGA adapter beside its own vmware-svga device, so the guest has "+
			"two displays:\n%s", mac)
	}
}

// AND THE ISO IS THE MEDIUM, NOT A KERNEL. A boot with `iso:` and no kernel is the shape that runs
// the installer's own boot chain, which is the only one that proves anything about the disk.
func TestAnAndroidInstallBootsItsISO(t *testing.T) {
	dir := t.TempDir()
	disk := filepath.Join(dir, "d.qcow2")
	iso := filepath.Join(dir, "i.iso")
	for _, f := range []string{disk, iso} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b := Boot{OS: Android, Disk: disk, ISO: iso}
	if err := b.Validate(); err != nil {
		t.Fatalf("an android ISO boot was refused: %v", err)
	}
	if !strings.Contains(profileArgs(t, "android"), "{{iso}}") {
		t.Error("android's qemu.args do not attach `{{iso}}`, so the installer's own boot chain is " +
			"never started and the stage cannot install anything")
	}
}

// THE ADAPTER IS NOT A PREFERENCE, AND IT IS WRITTEN WHERE THE SHAPE CAN BE READ.
//
// A std VGA on an Android guest gives it a framebuffer with no DRM node; HWC=drm_minigbm opens
// /dev/dri/card0, finds nothing, and Android draws nothing at all. The screen stops on initrd's last
// line and the console says nothing, which is why this was chased at 240 s, 600 s and 600 s again
// before the cause was found.
//
// THE OLD TEST ALSO ASSERTED THAT A CALLER COULD NOT TALK ANDROID OUT OF IT. There is nothing to talk
// it out of now: the adapter is a line in a file, and a walk that wants a different one edits the
// shape rather than passing a flag the engine overrules.
func TestAndroidRefusesAnyOtherAdapter(t *testing.T) {
	args := profileArgs(t, "android")
	if strings.Contains(args, "VGA,xres=") {
		t.Errorf("android's qemu.args name the std `-device VGA` form as well as virtio-vga, so the "+
			"guest gets a framebuffer with no DRM node:\n%s", args)
	}
	if strings.Contains(args, `"-vga"`) {
		t.Errorf("android's qemu.args use `-vga`, which cannot carry a resolution and on a microvm "+
			"creates no device at all:\n%s", args)
	}
}

// profileArgs is a shape's default profile, flattened, for a test that asserts on its text.
func profileArgs(t *testing.T, shape string) string {
	return profileArgsNamed(t, shape, "install")
}

// profileArgsNamed is one named profile's arguments, flattened.
//
// `install` IS THE DEFAULT FOR THESE TESTS because the adapter and the media order are what an
// INSTALL asserts; a disk boot has no medium to order.
func profileArgsNamed(t *testing.T, shape, name string) string {
	t.Helper()
	sh, ok := Shapes().Lookup(shape)
	if !ok {
		t.Skipf("no %q shape here: it lives in easyvm-%s/shapes, and this checkout does not have it",
			shape, shape)
	}
	prof, err := sh.Profile(name)
	if err != nil {
		t.Fatalf("%s: %v", shape, err)
	}
	var out []string
	out = append(out, prof.Machine)
	for _, pair := range prof.Args {
		out = append(out, pair...)
	}
	for _, pair := range prof.KernelArgs {
		out = append(out, pair...)
	}
	return strings.Join(out, " ")
}
