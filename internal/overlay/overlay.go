// Package overlay is a read-only base disk plus a throwaway qcow2 diff over it.
//
// A 27 GiB macOS disk takes fifteen minutes to download and is the only copy of half a day's work.
// Every attempt to test something against it wants one thing: START FROM THE SAME BYTES. The two
// ways of getting close are both wrong:
//
//	move it           the base is consumed by the first run, and every later attempt starts from a
//	                  disk the previous one already wrote to
//	copy it           a 27 GiB copy per attempt, and it does not actually guarantee anything - a
//	                  run that corrupts its copy is invisible unless something compares the two,
//	                  and nothing does
//
// A QCOW2 BACKING CHAIN IS THE THING ITSELF. `qemu-img create -b base.qcow2 overlay.qcow2` reads
// through to the base for every cluster it does not have and writes every new cluster into itself.
// While a backing file is in use qemu opens the parent READ-ONLY, so a run cannot corrupt it even
// if it tries - and deleting the overlay restores the disk to its published state exactly, rather
// than approximately.
//
// WHAT IT COSTS: one overlay per run, growing only with what that run writes. A macOS boot writes a
// few hundred MiB to a couple of GiB; the base stays 27 GiB and is never copied.
//
// AND IT IS WHAT MAKES `discard=unmap` SAFE. The flag frees clusters in the file qemu was given,
// which is now the diff; the base is a read-only parent that never sees a TRIM. Handing blocks back
// from a published artifact directly is the case the Python implementation could not explain away
// (see the note in machine.Boot.installing), and an overlay removes it rather than working around
// it.
package overlay

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Overlay is a writable qcow2 whose parent is a base disk that must not change.
type Overlay struct {
	// Base is the published disk this run reads through to.
	Base string
	// Path is the diff qemu is given.
	Path string

	// baseSize and baseMtime are the base's identity before anything opened it, so Close can prove
	// it was not written to. "I did not write to it" is an assumption, and this file exists to stop
	// that kind of assumption.
	baseSize  int64
	baseMtime int64
}

// Create makes the overlay and returns it.
//
// A LEFTOVER OVERLAY IS DELETED, NOT REUSED. It is a disk a previous attempt wrote to, and starting
// from it would carry that attempt's half-finished work forward - which is the exact failure this
// package exists to prevent.
func Create(base, work, name string) (*Overlay, error) {
	abs, err := filepath.Abs(base)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("no base disk at %s: %w - this run starts from a published disk, "+
			"so it has to be fetched first", abs, err)
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("the base disk at %s is a directory", abs)
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return nil, err
	}
	if name == "" {
		name = "run-overlay.qcow2"
	}
	path := filepath.Join(work, name)
	o := &Overlay{Base: abs, Path: path, baseSize: fi.Size(), baseMtime: fi.ModTime().UnixNano()}

	if _, err := os.Stat(path); err == nil {
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("removing the previous overlay: %w", err)
		}
	}

	// `-F qcow2` NAMES THE PARENT'S FORMAT, and qemu refuses to create a chain without it: the
	// backing file's format cannot be inferred, and guessing it is how a raw image gets opened as
	// if it were a qcow2.
	out, err := runQemuImg("create", "-f", "qcow2", "-F", "qcow2", "-b", abs, path)
	if err != nil {
		return nil, fmt.Errorf("could not create an overlay over %s: %w (%s)",
			filepath.Base(abs), err, out)
	}
	return o, nil
}

// Close deletes the diff and proves the base is the same file it was.
//
// THE VERIFICATION IS THE POINT, not the deletion. A run that somehow reached through to the base
// must say so, rather than leaving a quietly modified artifact behind for the next stage to read.
// It returns what the run wrote, for a log line.
func (o *Overlay) Close() (written int64, err error) {
	if fi, statErr := os.Stat(o.Path); statErr == nil {
		written = fi.Size()
	}
	// A DELETE THAT FAILS IS NOT A FAILED RUN - but a base that changed is.
	_ = os.Remove(o.Path)
	return written, o.Verify()
}

// Verify fails loudly if the base is not exactly what Create saw.
func (o *Overlay) Verify() error {
	fi, err := os.Stat(o.Base)
	if err != nil {
		return fmt.Errorf("=== THE BASE DISK IS GONE: %s ===\n  the base is a published "+
			"artifact; a run that removed it cannot be published from", o.Base)
	}
	size, mtime := fi.Size(), fi.ModTime().UnixNano()
	if size != o.baseSize || mtime != o.baseMtime {
		return fmt.Errorf("=== THE BASE DISK CHANGED DURING THIS RUN ===\n  %s\n  before: "+
			"size %d mtime_ns %d\n  after:  size %d mtime_ns %d\n  the base is a published "+
			"artifact and must never be written to", o.Base, o.baseSize, o.baseMtime, size, mtime)
	}
	return nil
}

// Exists reports whether the diff is still on disk, so a caller can tell "already released" from
// "never created".
func (o *Overlay) Exists() bool {
	if o == nil {
		return false
	}
	_, err := os.Stat(o.Path)
	return err == nil
}

// Owns reports whether path is this overlay's diff, which is how a `compress:` step tells a run on
// a throwaway diff from one that wrote the disk itself.
func (o *Overlay) Owns(path string) bool {
	if o == nil || path == "" {
		return false
	}
	a, err1 := filepath.Abs(path)
	if err1 != nil {
		return false
	}
	return a == o.Path
}

// runQemuImg runs qemu-img and returns its own words on failure.
//
// IT NAMES THE BINARY RATHER THAN TRUSTING PATH. The Python implementation lost two runs to an
// image that put an emulator's 2018 qemu-img ahead of /usr/bin on PATH; that call answered
//
//	qemu-img: Invalid parameter 'compression_type'
//
// about a disk this repository had just compressed. This build's Containerfile has no such entry,
// so the bare name resolves correctly - but naming it is still right, and `versions` lists it.
func runQemuImg(args ...string) (string, error) {
	bin, err := exec.LookPath("qemu-img")
	if err != nil {
		return "", fmt.Errorf("qemu-img is not installed: %w", err)
	}
	cmd := exec.Command(bin, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
