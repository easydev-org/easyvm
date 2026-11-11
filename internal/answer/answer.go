// Package answer builds the small CD that carries Windows' answer file and its drivers.
//
// WHY NOT REBUILD THE INSTALL ISO. The obvious approach is to add \Autounattend.xml to the install
// ISO's root and rebuild it. xorriso cannot add a file to an ISO in place, so that costs a full
// remaster of 7.7 GiB - and the Python implementation's first attempt at the short form of it
// produced a 448 KB ISO: a boot record with the answer file and NONE of the installer's files.
// qemu started it happily, found nothing to boot, and the bake sat at the arrival check until it
// was cancelled.
//
// WINDOWS DOES NOT REQUIRE THE FILE ON THE INSTALL MEDIUM. Setup looks for \Autounattend.xml at the
// root of ANY removable drive it can see, so a second tiny ISO is enough: a few hundred KB written
// instead of 7.7 GiB, seconds instead of minutes, and no dependency on El Torito being reproduced
// correctly. That is the documented way to supply an answer file for a read-only install source.
//
// THE DRIVERS GO ON THE SAME DISC, IN $WinPEDriver$. That directory name is the mechanism that
// needs no configuration: Setup scans removable media for it and loads what it finds. Attaching the
// virtio-win CD is NOT enough - Setup loads a storage driver from it only when told to, and the
// answer file has no DriverPaths. Without $WinPEDriver$ the drive list is empty and Setup stops at
// a dialog nobody is watching.
package answer

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// WinPEDriverDir is the name Setup looks for. Not a configured path - the directory name IS the
// configuration.
const WinPEDriverDir = "$WinPEDriver$"

// winPEDrivers are the drivers windowsPE must load before it can install anything.
//
// ONE OF THESE IS NOT FOR windowsPE AT ALL, and the list keeps it because the driver CD is the one
// place a driver can be added:
//
//	viostor   the disk. Without it windowsPE cannot see the target and Setup stops.
//	vioscsi   the same for a SCSI-attached disk; harmless when the disk is virtio-blk.
//	NetKVM    the network. Server 2025 has no in-box driver for virtio-net.
//	Balloon   memory ballooning. Nothing depends on it.
//
// vioserial IS DELIBERATELY ABSENT. Its INF is Class=System, and a System-class INF on this CD is
// not ignored by windowsPE - it is a driver for a device class the image enumerates, so Setup
// stages it into the offline image and the installed system loads it on the boot after the file
// copy. Measured (run 42839116205): "Installing 54%", TianoCore, then "The computer restarted
// unexpectedly or encountered an unexpected error". It is installed LATER, by pnputil over ssh,
// which is what vm-configure does.
var winPEDrivers = []string{"viostor", "vioscsi", "NetKVM", "Balloon"}

// driverVersions are tried in order. w11 first because that is the guest; w10 and 2k19 are there
// because an older virtio-win ISO may not carry a w11 tree, and the driver is compatible.
var driverVersions = []string{"w11", "w10", "2k19"}

// Spec says what to build.
type Spec struct {
	// Template is the answer file to substitute placeholders into. It is NOT modified.
	Template string
	// Out is the ISO to write.
	Out string
	// VirtioISO is where the drivers come from. Empty builds an answer CD with no drivers, which
	// is only correct for an install that does not need them.
	VirtioISO string
	// Values are the placeholders and what to put in them. Every placeholder in the template must
	// appear here, or the build is refused - see substitute.
	Values map[string]string
	// Label is the ISO's volume name. It changes nothing about what Setup finds, and makes a log
	// with three CD-ROMs readable.
	Label string
}

// Placeholders are the names a template may use, so an error can list the real vocabulary.
func Placeholders() []string {
	names := make([]string, 0, len(knownPlaceholders))
	for k := range knownPlaceholders {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// knownPlaceholders is the vocabulary, and it is closed on purpose: a template that invents one is
// a template whose value nobody supplies, and Setup receives the literal text.
var knownPlaceholders = map[string]bool{
	"__ADMIN_USER__":          true,
	"__ADMIN_PASSWORD__":      true,
	"__COMPUTER_NAME__":       true,
	"__WINDOWS_IMAGE_INDEX__": true,
}

// Build writes the answer ISO.
func Build(spec Spec, log func(string)) error {
	if spec.Template == "" {
		return fmt.Errorf("no answer file template was named")
	}
	if _, err := os.Stat(spec.Template); err != nil {
		return fmt.Errorf("no answer file at %s: %w", spec.Template, err)
	}
	if spec.Out == "" {
		return fmt.Errorf("no output path for the answer ISO")
	}
	if spec.Label == "" {
		spec.Label = "ANSWER"
	}

	// XORRISO IS REQUIRED AND ITS ABSENCE IS NAMED. The Python implementation's note: a wrong flag
	// is a failure only at run time, and its first version passed `-rock on` - which xorriso does
	// not have, because Rock Ridge is produced by default - and died with exit 5 ninety seconds in,
	// after downloading 7.7 GiB. The argv is a separate function so a test can assert it.
	if _, err := exec.LookPath("xorriso"); err != nil {
		return fmt.Errorf("xorriso is not installed: %w. The answer CD is an ISO9660 image with "+
			"a Joliet tree, and building one by hand is how a filename arrives truncated", err)
	}

	stage, err := os.MkdirTemp("", "answeriso-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)

	body, err := os.ReadFile(spec.Template)
	if err != nil {
		return err
	}
	text, err := substitute(string(body), spec.Values)
	if err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(spec.Template), err)
	}
	// WINDOWS .cmd FILES MUST BE CRLF. cmd.exe treats a bare LF as part of the line in some paths,
	// and a label such as `:state_done` followed by LF is not recognised. XML has no such
	// requirement, and rewriting it only risks a BOM or an encoding question.
	if err := os.WriteFile(filepath.Join(stage, "Autounattend.xml"),
		[]byte(strings.ReplaceAll(text, "\r\n", "\n")), 0o644); err != nil {
		return err
	}

	if spec.VirtioISO != "" {
		winpe := filepath.Join(stage, WinPEDriverDir)
		if err := os.MkdirAll(winpe, 0o755); err != nil {
			return err
		}
		found, err := ExtractDrivers(spec.VirtioISO, winpe)
		if err != nil {
			return err
		}
		log(fmt.Sprintf("  drivers on the answer CD: %s", strings.Join(found, ", ")))
	}

	if err := os.MkdirAll(filepath.Dir(spec.Out), 0o755); err != nil {
		return err
	}
	// A PREVIOUS ISO IS REMOVED FIRST: xorriso refuses an existing output file, and a stale one
	// would be published with a fresh log line saying it was rebuilt.
	_ = os.Remove(spec.Out)

	log(fmt.Sprintf("  building the answer ISO -> %s", spec.Out))
	cmd := exec.Command("xorriso", argv(spec.Out, stage, spec.Label)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("xorriso: %v (%s)", err, firstLine(string(out)))
	}
	fi, err := os.Stat(spec.Out)
	if err != nil {
		return fmt.Errorf("xorriso reported success and %s is not there", spec.Out)
	}
	// A 448 KB ISO IS THE FAILURE THIS PACKAGE EXISTS TO AVOID: a boot record with the answer file
	// and none of the installer's files. This ISO is small on purpose, so the check is not a size
	// floor but a NON-EMPTY floor with the reason attached.
	if fi.Size() < 100<<10 {
		return fmt.Errorf("the answer ISO is %d bytes, which is smaller than an ISO9660 volume "+
			"with one file and a driver tree; xorriso wrote a boot record only", fi.Size())
	}
	log(fmt.Sprintf("  %s: %d KiB", filepath.Base(spec.Out), fi.Size()>>10))
	return nil
}

// argv is the xorriso command line, pure so a test can assert it without running it.
//
// THERE IS NO `-rock` OPTION. Rock Ridge is produced by default, and asking for it is an error
// rather than a no-op - measured, at the cost of a bake that died with exit 5 after a 7.7 GiB
// download.
func argv(out, stage, label string) []string {
	return []string{
		"-outdev", out,
		"-volid", label,
		// Joliet lets Windows read long filenames.
		"-joliet", "on",
		// `-map` on a directory inserts its whole subtree; the destination "/" puts the file at
		// the image root, which is where Setup looks.
		"-map", stage, "/",
		"-commit",
	}
}

// substitute replaces every placeholder and refuses to leave one behind.
//
// A LEFTOVER PLACEHOLDER IS A NINE-MINUTE WAIT FOR A PAGE NOBODY CLICKS. The Python implementation
// learned this the expensive way: __WINDOWS_IMAGE_INDEX__ was missing from its substitution list
// while the template used it, so Setup received a literal string where it expected the edition
// index, could not resolve it, and abandoned the unattended flow at "Enter a product key" - 131
// identical frames over nine minutes.
func substitute(text string, values map[string]string) (string, error) {
	for place, value := range values {
		if !knownPlaceholders[place] {
			return "", fmt.Errorf("%q is not a placeholder this build knows; the vocabulary is %s",
				place, strings.Join(Placeholders(), ", "))
		}
		text = strings.ReplaceAll(text, place, value)
	}
	var leftover []string
	for place := range knownPlaceholders {
		if strings.Contains(text, place) {
			leftover = append(leftover, place)
		}
	}
	if len(leftover) > 0 {
		sort.Strings(leftover)
		return "", fmt.Errorf("%s would be left in the answer file after substitution. Setup "+
			"receives the literal text, cannot resolve it, and falls back to asking a human - a "+
			"page nobody clicks while the stage waits out its timeout",
			strings.Join(leftover, ", "))
	}
	return text, nil
}

// ExtractDrivers copies the windowsPE-loadable drivers out of the virtio-win ISO into stage.
//
// THE ISO'S LAYOUT IS
//
//	viostor/w11/amd64/*.inf  *.sys  *.cat
//	vioscsi/w11/amd64/...
//	NetKVM/w11/amd64/...
//
// and each driver's files are moved up to `stage/<driver>/` so the tree is one level shallower.
// Setup recurses, but a single flat directory of conflicting `*.inf` names does not work, so the
// per-driver directory stays.
//
// 7z RATHER THAN A MOUNT: reading an ISO as an archive needs no privileges.
func ExtractDrivers(virtioISO, stage string) ([]string, error) {
	if _, err := os.Stat(virtioISO); err != nil {
		return nil, fmt.Errorf("no virtio-win ISO at %s: %w", virtioISO, err)
	}
	sevenZip, err := exec.LookPath("7z")
	if err != nil {
		return nil, fmt.Errorf("7z is not installed: %w. The virtio-win ISO carries a Joliet "+
			"tree, and extracting it by hand produces 8.3 filenames that do not match the driver "+
			"the answer file names", err)
	}

	var found []string
	for _, driver := range winPEDrivers {
		for _, version := range driverVersions {
			member := driver + "/" + version + "/amd64"
			cmd := exec.Command(sevenZip, "x", "-y", "-o"+stage, virtioISO, member+"/*")
			out, err := cmd.CombinedOutput()
			source := filepath.Join(stage, driver, version, "amd64")
			if err != nil || !dirHasFiles(source) {
				continue
			}
			target := filepath.Join(stage, driver)
			if err := os.MkdirAll(target, 0o755); err != nil {
				return nil, err
			}
			entries, err := os.ReadDir(source)
			if err != nil {
				return nil, err
			}
			for _, e := range entries {
				if err := os.Rename(filepath.Join(source, e.Name()),
					filepath.Join(target, e.Name())); err != nil {
					return nil, err
				}
			}
			// The empty version directories 7z created are not wanted in the image.
			_ = os.RemoveAll(filepath.Join(stage, driver, version))
			found = append(found, driver)
			_ = out
			break
		}
	}

	if len(found) == 0 {
		return nil, fmt.Errorf("no drivers were extracted from %s. Looked for %s under %s "+
			"amd64. Without viostor, windowsPE cannot see the virtio disk and Setup stops at a "+
			"dialog nobody clicks", virtioISO, strings.Join(winPEDrivers, ", "),
			strings.Join(driverVersions, "/"))
	}
	return found, nil
}

func dirHasFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}
