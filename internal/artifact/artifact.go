// Package artifact is the LIBRARY of named pictures a run produced.
//
// # WHAT GOES IN IT, AND WHAT DOES NOT
//
// A picture enters the library in exactly two ways, and both of them are a PERSON or a FILE asking
// for a picture BY NAME:
//
//	see:        {name: shot}          one capture, named, so every later check can ask about it
//	screenshot: {path: out/x.png}     one capture, written where the file says
//
// IT IS NOT A FRAME DUMP. The recorder takes a screendump every second and a forty-minute stage is
// two thousand of them; those are the RECORDING's frames, they are replaced a second later, and
// keeping them in a library would be 2 GB of pictures nobody named. MEASURED on one pod's work
// directory: 542 frames, 541 MB.
//
// # WHY CONTENT-ADDRESSED
//
// MEASURED: of the first 200 frames of a real run, FIFTY-FIVE were unique - 72.5% were byte-identical
// repeats, because a screen that is not changing is captured again and again. A library keyed by
// content stores one copy and answers every reference with it.
//
// IT ALSO FIXES A REAL DEFECT. A journal record that stored `frames/frame-000123.png` pointed at a
// file that the next capture OVERWROTE, so a report read ten seconds later showed a different picture
// under the same name. A content address cannot do that: the name IS the bytes.
//
// # WHY PER RUN
//
// A library belongs to one run and is published with it. Two runs that captured the same login screen
// store it twice, and that is the price of the property that matters: a published artifact reproduces
// exactly what one run saw. A shared library would need reference counting and a deletion policy to
// answer "is this picture still anyone's", and the answer would change when an unrelated run was
// cleaned up.
package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// Store is one run's library, on disk.
//
// THE LAYOUT IS TWO LEVELS OF HEX, because a directory with two thousand entries is slow on some
// filesystems and a directory with sixteen is not:
//
//	artifact/<first two hex>/<sha256>.png
type Store struct {
	mu  sync.Mutex
	dir string
	// seen is what this run has already stored, so a repeat costs no write at all.
	//
	// THE HASH IS COMPUTED FROM THE BYTES, not from the frame's path: two captures of an unchanged
	// screen are different files with the same content, and that is the case this exists for.
	seen map[string]bool
}

// Open prepares a library in dir. It does not require the directory to be empty: a run resumed in the
// same work directory keeps the pictures it already stored.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating the artifact library: %w", err)
	}
	s := &Store{dir: dir, seen: map[string]bool{}}
	// THE INDEX IS REBUILT FROM THE FILES, not from a side file that could disagree with them. A
	// directory listing is the truth about what is stored.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		inner, err := os.ReadDir(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		for _, f := range inner {
			name := f.Name()
			if filepath.Ext(name) == ".png" {
				s.seen[trimExt(name)] = true
			}
		}
	}
	return s, nil
}

// Put stores the bytes at path and returns the name they are known by.
//
// THE RETURNED NAME IS THE CONTENT HASH. A caller stores it in the journal and serves it later with
// `GET /api/artifact?h=<name>`; the address never goes stale.
//
// AN ALREADY-STORED PICTURE COSTS ONE READ AND NO WRITE, which is what makes a loop that captures the
// same screen eight times cheap rather than eight copies.
func (s *Store) Put(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	key := sum[:16]

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen[key] {
		return key, nil
	}
	dst := s.file(key)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	// A COPY RATHER THAN A RENAME: the source is the recorder's frame, and moving it would take the
	// picture out of the recording that also uses it.
	if err := copyFile(path, dst); err != nil {
		return "", err
	}
	s.seen[key] = true
	return key, nil
}

// Path returns where a stored picture is, and whether it exists.
//
// IT REFUSES A NAME THAT IS NOT SIXTEEN HEX CHARACTERS, which is the whole path-traversal defence:
// a key cannot be `../../etc/passwd` because it cannot be anything but hex.
func (s *Store) Path(key string) (string, bool) {
	if !validKey(key) {
		return "", false
	}
	p := s.file(key)
	if _, err := os.Stat(p); err != nil {
		return "", false
	}
	return p, true
}

// file is where a key lives.
func (s *Store) file(key string) string {
	return filepath.Join(s.dir, key[:2], key+".png")
}

// validKey reports whether a name could be a content address.
func validKey(key string) bool {
	if len(key) != 16 {
		return false
	}
	for _, c := range key {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

// copyFile copies src to dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

// trimExt drops a filename's extension.
func trimExt(name string) string {
	if i := len(name) - 4; i > 0 && name[i] == '.' {
		return name[:i]
	}
	return name
}

// Count is how many distinct pictures the library holds, for a log line.
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seen)
}