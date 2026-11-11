package overlay

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A REAL QCOW2, OR A SKIP. This package's whole job is to make qemu-img do one thing and to prove
// the file it read through was untouched; a test that faked qemu-img would prove that the fake
// works. So the test either runs the real binary or says it could not.
func haveQemuImg(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("qemu-img")
	if err != nil {
		t.Skip("qemu-img is not installed, so an overlay cannot be created here")
	}
	return bin
}

// base creates a small qcow2 to read through.
func base(t *testing.T) string {
	t.Helper()
	bin := haveQemuImg(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "base.qcow2")
	out, err := exec.Command(bin, "create", "-f", "qcow2", p, "64M").CombinedOutput()
	if err != nil {
		t.Fatalf("qemu-img create: %v (%s)", err, out)
	}
	return p
}

// AN OVERLAY READS THROUGH TO THE BASE AND WRITES ONLY ITSELF.
//
// THE ASSERTION THAT MATTERS IS THE BASE'S IDENTITY. "I did not write to it" is an assumption, and
// this package exists to stop that kind of assumption: the base's size and mtime are recorded
// before qemu opens it and re-checked after the diff is deleted.
func TestTheBaseIsNeverWrittenTo(t *testing.T) {
	haveQemuImg(t)
	disk := base(t)
	before, err := os.Stat(disk)
	if err != nil {
		t.Fatal(err)
	}

	o, err := Create(disk, t.TempDir(), "run-overlay.qcow2")
	if err != nil {
		t.Fatal(err)
	}
	if o.Path == disk {
		t.Fatal("the overlay is the base; a run would write the published artifact directly")
	}

	// WRITING INTO THE OVERLAY MUST NOT REACH THE BASE. This is what qcow2's backing chain
	// guarantees - qemu opens the parent read-only - and writing a byte at offset 0 is enough to
	// prove the file is not shared.
	f, err := os.OpenFile(o.Path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(make([]byte, 1<<20), 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if written, err := o.Close(); err != nil {
		t.Fatalf("close: %v", err)
	} else if written == 0 {
		t.Fatal("the overlay reported nothing written although a megabyte was; the size it " +
			"reports is the number a log line needs")
	}

	after, err := os.Stat(disk)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("the base changed: before %d/%v, after %d/%v", before.Size(),
			before.ModTime(), after.Size(), after.ModTime())
	}
	if _, err := os.Stat(o.Path); !os.IsNotExist(err) {
		t.Fatalf("the diff survived close(); it is throwaway and a leftover is what the next "+
			"run would start from: %v", err)
	}
}

// A LEFTOVER DIFF IS DELETED, NOT REUSED.
//
// It is a disk a previous attempt wrote to. Reusing it would carry that attempt's half-finished
// work into this one, which is the failure the whole package exists to prevent.
func TestALeftoverDiffIsDeletedRatherThanReused(t *testing.T) {
	haveQemuImg(t)
	disk := base(t)
	work := t.TempDir()

	first, err := Create(disk, work, "run-overlay.qcow2")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(first.Path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(make([]byte, 4<<20), 0); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	big := first.Path
	fi, err := os.Stat(big)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() < 1<<20 {
		t.Fatalf("the deliberate write did not land; the file is %d bytes", fi.Size())
	}

	second, err := Create(disk, work, "run-overlay.qcow2")
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(second.Path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() >= fi.Size() {
		t.Fatalf("the second overlay is as big as the one carrying 4 MiB of a previous run "+
			"(%d bytes); it was reused rather than replaced", after.Size())
	}
}

// A BASE THAT CHANGED IS A FAILED RUN, SAID OUT LOUD.
//
// The size and mtime are the identity, so touching the file is enough - a run that reached through
// to the base is not something to discover from the artifact a week later.
func TestABaseThatChangedIsRefused(t *testing.T) {
	haveQemuImg(t)
	disk := base(t)
	o, err := Create(disk, t.TempDir(), "run-overlay.qcow2")
	if err != nil {
		t.Fatal(err)
	}
	// SOMETHING ELSE WRITES THE BASE, which is the thing this must catch.
	if err := os.WriteFile(disk, []byte("not a qcow2 any more"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = o.Close()
	if err == nil {
		t.Fatal("a base that changed during the run must be reported; the base is a published " +
			"artifact and the next stage would read whatever is left of it")
	}
	for _, want := range []string{"CHANGED", disk} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the failure should name %q; got %v", want, err)
		}
	}
}

// A MISSING BASE IS REFUSED BEFORE ANYTHING IS CREATED, with the sentence that says what to do.
func TestAMissingBaseSaysItMustBeFetched(t *testing.T) {
	_, err := Create(filepath.Join(t.TempDir(), "absent.qcow2"), t.TempDir(), "run-overlay.qcow2")
	if err == nil {
		t.Fatal("a missing base must be refused")
	}
	if !strings.Contains(err.Error(), "fetched") {
		t.Fatalf("the refusal should say the disk has to be fetched first; got %v", err)
	}
}

// Owns is how a `compress:` step tells a run on a diff from one that wrote the disk itself.
func TestOwnsRecognisesOnlyItsOwnDiff(t *testing.T) {
	o := &Overlay{Base: "/disks/base.qcow2", Path: "/work/run-overlay.qcow2"}
	if !o.Owns("/work/run-overlay.qcow2") {
		t.Fatal("Owns did not recognise its own diff")
	}
	if o.Owns("/disks/base.qcow2") {
		t.Fatal("Owns called the base its own diff; publishing would flatten the wrong file")
	}
	var none *Overlay
	if none.Owns("/work/run-overlay.qcow2") {
		t.Fatal("a nil overlay claimed a diff")
	}
}
