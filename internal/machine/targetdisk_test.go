package machine

import (
	"strings"
	"testing"
)

// A SMOKE BOOT MUST OPEN THE DIFF, NOT THE DISK IT WAS HANDED.
//
// MEASURED AS A REGRESSION WHEN THE PLACEHOLDER RENDERER WAS DELETED:
//
//	before=570161664   after=571211776     the BASE grew by 1 MB
//	run-overlay.qcow2 = 196672 bytes       the diff stayed a bare qcow2 header
//
// The overlay was created and closed having never been opened, because qemu was handed the base. The
// run still "succeeded" - which is the dangerous half: the published artifact was the one the run
// wrote to, and every smoke walk published a disk it had modified.
func TestReplaceTargetDiskPointsQemuAtTheDiff(t *testing.T) {
	work := "/work"
	cases := []struct {
		name    string
		fileSp  string // what the walk wrote in `file=`
		running string
		want    string
	}{
		{"the work directory's own name", "disk.qcow2", "run-overlay.qcow2", "run-overlay.qcow2"},
		{"an absolute path from --boot-disk", "/work/disk.qcow2", "run-overlay.qcow2", "run-overlay.qcow2"},
		// THE DIFF ALWAYS LIVES IN THE WORK DIRECTORY, so it is always spelled relative to it -
		// even when the BASE came from somewhere else. qemu runs there.
		{"an absolute path outside work", "/disks/pulled/x.qcow2", "/work/run-overlay.qcow2",
			"run-overlay.qcow2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			target := c.fileSp
			args := []string{"-drive", "file=" + c.fileSp + ",if=none,format=qcow2,id=target"}
			got := ReplaceTargetDisk(args, target, c.running, work)
			want := "file=" + c.want + ",if=none,format=qcow2,id=target"
			if got[1] != want {
				t.Fatalf("got %q, want %q", got[1], want)
			}
		})
	}
}

// THE MEDIA ARE NOT THE TARGET, and rewriting one would point an installer at a disk.
func TestReplaceTargetDiskLeavesMediaAlone(t *testing.T) {
	work := "/work"
	args := []string{
		"-drive", "file=windows.iso,media=cdrom,readonly=on,if=none,id=media0",
		"-drive", "file=disk.qcow2,if=none,format=qcow2,id=target",
		"-drive", "file=virtio-win.iso,media=cdrom,readonly=on,if=none,id=media2",
	}
	got := ReplaceTargetDisk(args, "disk.qcow2", "run-overlay.qcow2", work)
	if !strings.Contains(got[1], "windows.iso") {
		t.Fatalf("the installer medium was rewritten: %q", got[1])
	}
	if !strings.Contains(got[3], "run-overlay.qcow2") {
		t.Fatalf("the target was not rewritten: %q", got[3])
	}
	if !strings.Contains(got[5], "virtio-win.iso") {
		t.Fatalf("the driver medium was rewritten: %q", got[5])
	}
}

// NO DIFF MEANS NOTHING TO DO, and the arguments come back untouched.
func TestReplaceTargetDiskIsANoOpWithoutADiff(t *testing.T) {
	args := []string{"-drive", "file=disk.qcow2,if=none,id=target"}
	got := ReplaceTargetDisk(args, "disk.qcow2", "disk.qcow2", "/work")
	if got[1] != args[1] {
		t.Fatalf("a boot with no diff rewrote its own disk: %q", got[1])
	}
}
