// Package opencore builds the firmware a macOS guest boots through.
//
// A MAC HAS TOLD ITS NIC WHICH FIRMWARE IT IS. On real hardware that is Apple's own; here it is
// OpenCore - a bootloader in a 256 MiB FAT32 image inside a GPT partition, started by OVMF as if it
// were an operating system. macOS then reads the SMC key, the serial and the board serial to decide
// it is on Apple hardware, and every one of those is a value this package writes.
//
// WHY THIS IS FOUR ASSETS AND NOT ONE. OpenCore's own release archive is the bootloader and nothing
// else: BOOTx64.efi and OpenCore.efi, with no kexts, no drivers and no config. The missing half -
// Lilu, VirtualSMC, WhateverGreen, the VoodooPS2 input drivers and the device properties that were
// arrived at by somebody else's runs - lives in LongQT-sea's template ISO. OVMF is the third thing,
// and it is TWO files: the code, which is immutable, and the variable store, which is not.
//
// ALL FOUR ARE PINNED BY SHA-256. An OpenCore or an OVMF that silently upgraded would produce a disk
// that boots today and stops booting when OpenCore changes its plist schema or OVMF changes its GOP
// mode - which is the class of failure that costs a day to notice.
package opencore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/easydev-org/easyvm/internal/oci"
)

// OSK is Apple's SMC key, obfuscated the way every hackintosh does it.
//
// IT IS NOT A SECRET AND NOT PER GUEST. It is the value macOS reads from the SMC device to decide
// whether the firmware is Apple's, and it has been the same string since 2009 - it is in OSX-KVM,
// in dockur/macos and in Apple's own diagnostic tooling.
const OSK = "ourhardworkbythesewordsguardedpleasedontsteal(c)AppleComputerInc"

// The boot image's geometry, COPIED FROM dockur/macos RATHER THAN CHOSEN.
//
// THE TWO NUMBERS THAT MATTER ARE NOT ROUND: 256 MiB and a 4-sector cluster. FAT32 stops being
// FAT32 below 65,525 clusters - that is the only thing separating it from FAT16 - and cluster count
// is (data sectors / sectors per cluster), so both numbers move it:
//
//	dockur   256 MiB, -c 4   ->  ~130,000 clusters   valid
//	200 MiB, -c 8            ->      50,836 clusters  INVALID
//
// mtools wrote the invalid one anyway and read it back, so every check on this side passed. OVMF's
// EDK II FAT driver validates the format and refused to mount the partition, and the console said
//
//	BdsDxe: failed to load Boot0001 "UEFI Misc Device" ...: Not Found
//
// while the same firmware's mapping table listed that disk with its partition intact. Five runs
// went into the qemu arguments before the console log was read.
const (
	BootImageMB          = 256
	SectorSize           = 512
	ESPSectorsPerCluster = 4
	ESPStartSector       = 2048 // 1 MiB in: the conventional, and cluster-aligned, start
	GPTEntryArraySectors = 32   // 128 entries * 128 bytes = 16 KiB
	GuestWidth           = 1920
	GuestHeight          = 1080
	Fat32MinClusters     = 65525
)

// The ESP's type GUID, as GPT stores it: the first three fields little-endian, the rest as written.
// C12A7328-F81F-11D2-BA4B-00A0C93EC93B.
var espTypeGUID = []byte{0x28, 0x73, 0x2a, 0xc1, 0x1f, 0xf8, 0xd2, 0x11,
	0xba, 0x4b, 0x00, 0xa0, 0xc9, 0x3e, 0xc9, 0x3b}

// gptDiskGUID is the disk's own identifier. Fixed rather than random on purpose: this image is
// rebuilt on every run, and a stable identifier makes two builds comparable byte for byte.
var gptDiskGUID = []byte{0x1A, 0xCB, 0x1E, 0x00, 0x3B, 0x8F, 0x4B, 0x2A,
	0x86, 0xA4, 0xD9, 0x9E, 0xD2, 0x1D, 0xCA, 0xEB}

// Asset is one pinned download.
type Asset struct {
	// Tag is the ghcr tag this repository publishes it under.
	Tag string
	// SHA256 is the pin. A mismatch is fatal rather than a warning.
	SHA256 string
}

// Assets are the four files, with the pins from lib/images.py's MACOS_ASSETS.
//
// THE URLS ARE NOT HERE AND THAT IS THE POINT: a run fetches from ghcr, not from GitHub's release
// CDN with the job clock running. The URLs were used once, to derive these pins.
var Assets = map[string]Asset{
	"opencore": {
		Tag:    "macos-asset-opencore",
		SHA256: "2ffab6ebf58c7aefb0bcb3a1a385d207746823d6dd87d44bd666e1286939943e",
	},
	"opencore-template": {
		Tag:    "macos-asset-opencore-template",
		SHA256: "287328995d4198f1b05166f087d85bf7ef66bedafe150d17ad112ac8de60051d",
	},
	"ovmf-code": {
		Tag:    "macos-asset-ovmf-code",
		SHA256: "52a625db5a45329bf8005232db33e94781787d92ae8d088f2f73332dd4ded3a9",
	},
	"ovmf-vars-1920x1080": {
		Tag:    "macos-asset-ovmf-vars-1920x1080",
		SHA256: "6ed987af3a3c155be71665f510eae3e007eda9b8b94afd59d45e91c4a11565cc",
	},
}

// macserialMember is where the serial generator lives inside OpenCore's own archive.
//
// IT TRAVELS WITH THE PAYLOAD IT DESCRIBES rather than being a second download that can drift from
// it, and using it is not a shortcut: a hand-rolled serial LOOKS random but its last characters are
// a checksum over the model and the factory code, and macOS derives the board serial from the same
// data. A made-up one fails Apple's checksum and the guest stops booting when the NVRAM remembers it.
const macserialMember = "Utilities/macserial/macserial.linux"

// apfsDriverPath is where Apple keeps the APFS driver inside a recovery image.
const apfsDriverPath = "macOS Base System/usr/standalone/i386/apfs.efi"

// Identity is the machine's serial numbers, and it PERSISTS.
//
// MACOS BINDS AN INSTALLATION TO IT. A new serial per boot gives a disk that boots on the machine
// that made it and on nothing else - and worse, the NVRAM keeps the old identity, so the guest can
// end up disagreeing with itself and stopping at a boot loop that looks like a broken disk.
type Identity struct {
	Release     string `json:"release"`
	Model       string `json:"model"`
	Serial      string `json:"serial"`
	BoardSerial string `json:"board_serial"`
	UUID        string `json:"uuid"`
	MAC         string `json:"mac"`
	GeneratedAt string `json:"generated_at"`
}

// Result is what a build produced - the three paths the boot step names, plus the identity.
type Result struct {
	Boot     string
	ROM      string
	Vars     string
	Identity Identity
}

// Spec is what to build.
type Spec struct {
	// Release is the macOS version, for the identity record.
	Release string
	// Work is where everything goes.
	Work string
	// Recovery is the recovery image the APFS driver is extracted from. Empty means no driver is
	// installed, which produces a boot image the firmware can start and macOS cannot read.
	Recovery string
	// Model is the SMBIOS model macserial generates a serial for.
	Model string
}

// Build produces everything the VM needs, from the pins, into work. Idempotent.
func Build(spec Spec, log func(string)) (Result, error) {
	if spec.Work == "" {
		return Result{}, fmt.Errorf("opencore: no work directory")
	}
	if spec.Release == "" {
		spec.Release = "15"
	}
	if spec.Model == "" {
		spec.Model = "iMacPro1,1"
	}
	// THE RECOVERY IMAGE IS REFUSED HERE, BEFORE ANYTHING IS DOWNLOADED.
	//
	// IT IS NOT OPTIONAL, AND ITS ABSENCE IS SILENT AT THE END: the APFS driver is extracted from
	// that image, and a build without one produces a boot image the firmware starts and that cannot
	// read the system it installed. EnableJumpstart=true does not help - it HOOKS a driver the
	// firmware is expected to have, and OVMF has none - so the symptom is an installation that
	// completes and a picker listing only Reset NVRAM and Toggle SIP, with nothing in any log.
	//
	// Checked FIRST because the four assets are 20 MB of download: a caller that forgot to name the
	// recovery image should be told that in a second, not after the transfer and a build.
	if spec.Recovery == "" {
		return Result{}, fmt.Errorf("no recovery image: it is the installer AND the only source of "+
			"Apple's APFS driver (%s), which OpenCore cannot read an APFS partition without. A "+
			"macOS guest built without it installs a system it cannot boot", apfsDriverPath)
	}
	if _, err := os.Stat(spec.Recovery); err != nil {
		return Result{}, fmt.Errorf("no recovery image at %s: %w. It is an input to this build, "+
			"not just to the boot: %s comes out of it", spec.Recovery, err, apfsDriverPath)
	}
	if err := os.MkdirAll(spec.Work, 0o755); err != nil {
		return Result{}, err
	}

	archive, err := FetchPinned("opencore", filepath.Join(spec.Work, "opencore.zip"), log)
	if err != nil {
		return Result{}, err
	}
	templateISO, err := FetchPinned("opencore-template",
		filepath.Join(spec.Work, "opencore-template.iso"), log)
	if err != nil {
		return Result{}, err
	}
	rom, err := FetchPinned("ovmf-code", filepath.Join(spec.Work, "macos.rom"), log)
	if err != nil {
		return Result{}, err
	}
	varsSrc, err := FetchPinned("ovmf-vars-1920x1080",
		filepath.Join(spec.Work, "OVMF_VARS-1920x1080.fd"), log)
	if err != nil {
		return Result{}, err
	}

	efiDir := filepath.Join(spec.Work, "efi")
	if err := UnpackOpencore(archive, efiDir, spec.Work); err != nil {
		return Result{}, err
	}

	ident, err := IdentityFor(spec.Work, spec.Release, spec.Model, spec.Work, log)
	if err != nil {
		return Result{}, err
	}

	templateEFI := filepath.Join(spec.Work, "template-efi")
	if err := ExtractTemplateBoot(templateISO, templateEFI); err != nil {
		return Result{}, err
	}
	merged, err := MergeTemplate(efiDir, templateEFI)
	if err != nil {
		return Result{}, err
	}
	if err := WriteConfig(merged, templateEFI, ident, spec.Release); err != nil {
		return Result{}, err
	}

	// APFS AFTER THE CONFIG AND BEFORE THE BOOT IMAGE, and both halves of that order matter:
	// InstallAPFSDriver REGISTERS a driver in config.plist, so the config has to exist; and the
	// driver has to be INSIDE the image, so the image cannot exist yet.
	//
	// UNCONDITIONAL NOW. It used to be wrapped in `if spec.Recovery != ""`, which made the one case
	// that must never happen - a boot image with no APFS driver - indistinguishable from a normal
	// build. The emptiness is refused at the top of this function.
	if err := InstallAPFSDriver(merged, spec.Recovery, spec.Work, log); err != nil {
		return Result{}, err
	}

	boot := filepath.Join(spec.Work, "boot.img")
	if err := BuildBootImage(merged, boot, log); err != nil {
		return Result{}, err
	}

	// THE VARS FILE IS A WRITABLE COPY PER GUEST, made once and reused: macOS reboots inside one
	// VM, and a fresh copy each boot loses the boot entries the installed system just registered.
	vars := filepath.Join(spec.Work, "macos.vars")
	if _, err := os.Stat(vars); os.IsNotExist(err) {
		if err := copyFile(varsSrc, vars); err != nil {
			return Result{}, err
		}
		log("  macos.vars: writable copy of the 1920x1080 template")
	}

	return Result{Boot: boot, ROM: rom, Vars: vars, Identity: ident}, nil
}

// FetchPinned pulls one pinned asset from this repository's package, or reuses it, and verifies the
// sha256 either way.
//
// THE PIN IS CHECKED EVEN FOR A REUSED FILE. The registry guarantees the bytes it stored, not that
// they are the bytes this code expects: a tag can be overwritten.
func FetchPinned(name, dest string, log func(string)) (string, error) {
	asset, ok := Assets[name]
	if !ok {
		return "", fmt.Errorf("unknown macOS asset %q; the four are opencore, "+
			"opencore-template, ovmf-code, ovmf-vars-1920x1080", name)
	}

	if _, err := os.Stat(dest); err == nil {
		got, err := sha256File(dest)
		if err != nil {
			return "", err
		}
		if got == asset.SHA256 {
			log(fmt.Sprintf("  %s: present, sha256 matches", name))
			return dest, nil
		}
		log(fmt.Sprintf("  %s: cached file does not match the pin, fetching again", name))
		_ = os.Remove(dest)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	if err := pull(asset.Tag, dest); err != nil {
		return "", fmt.Errorf("%s is not in this repository's package: %w. It must be "+
			"published under that tag before a macOS stage can start", asset.Tag, err)
	}
	got, err := sha256File(dest)
	if err != nil {
		return "", err
	}
	if got != asset.SHA256 {
		_ = os.Remove(dest)
		return "", fmt.Errorf("%s: sha256 %s but the pin says %s. Either %s was overwritten "+
			"or the pin is stale - and an OpenCore that silently upgraded produces a disk that "+
			"boots today and stops booting when its plist schema changes",
			name, got, asset.SHA256, asset.Tag)
	}
	fi, _ := os.Stat(dest)
	log(fmt.Sprintf("  %s: %d MiB, sha256 verified", name, fi.Size()>>20))
	return dest, nil
}

// pull fetches one artifact into dest, through the same OCI client the disk pull uses.
func pull(tag, dest string) error {
	repository := os.Getenv("DISKS_REPOSITORY")
	if repository == "" {
		repository = "easydev-org/easydiskbake"
	}
	art := oci.Artifact{
		Repository: strings.ToLower(repository),
		Tag:        tag,
		Token:      os.Getenv("GITHUB_TOKEN"),
	}
	ctx, cancel := oci.WithTimeout(20 * time.Minute)
	defer cancel()
	return oci.Pull(ctx, art, dest, func(string) {})
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFile(from, to string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	dst, err := os.Create(to)
	if err != nil {
		return err
	}
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		return err
	}
	return dst.Sync()
}

// --- the parts that are separate because they are separate failures --------------------

// UnpackOpencore extracts the EFI tree from OpenCore's release archive into dest.
//
// ONLY X64 AND ONLY THE EFI TREE: the archive also holds IA32/EFI, which this guest cannot use, and
// the documentation, which would go into the boot image otherwise.
func UnpackOpencore(archive, dest, work string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	sevenZip, err := exec.LookPath("7z")
	if err != nil {
		return fmt.Errorf("7z is not installed: %w", err)
	}
	// THE ZIP'S OWN LAYOUT, extracted to a staging directory first so the X64 prefix can be
	// stripped: the tree the firmware wants is EFI/, not X64/EFI/.
	stage := filepath.Join(work, "opencore-unpacked")
	_ = os.RemoveAll(stage)
	cmd := exec.Command(sevenZip, "x", "-y", "-o"+stage, archive, "X64/*")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("unpacking %s: %v (%s)", filepath.Base(archive),
			err, firstLine(string(out)))
	}
	source := filepath.Join(stage, "X64", "EFI")
	if _, err := os.Stat(source); err != nil {
		return fmt.Errorf("%s has no X64/EFI tree; OpenCore's release layout changed",
			filepath.Base(archive))
	}
	if err := copyTree(source, filepath.Join(dest, "EFI")); err != nil {
		return err
	}
	return nil
}

// IdentityFor reads the identity a previous run wrote, or makes one.
//
// THE PERSISTENCE IS THE POINT, so the file is the source of truth rather than the generator.
func IdentityFor(work, release, model, toolDir string, log func(string)) (Identity, error) {
	path := filepath.Join(work, "identity.json")
	if body, err := os.ReadFile(path); err == nil {
		var ident Identity
		if err := json.Unmarshal(body, &ident); err != nil {
			return Identity{}, fmt.Errorf("identity.json at %s is not JSON: %w", path, err)
		}
		return ident, nil
	}

	serial, board, err := generateSerial(toolDir, model)
	if err != nil {
		return Identity{}, err
	}
	ident := Identity{
		Release:     release,
		Model:       model,
		Serial:      serial,
		BoardSerial: board,
		UUID:        strings.ToUpper(newUUID()),
		MAC:         generateMAC(),
		GeneratedAt: time.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}
	body, err := json.MarshalIndent(ident, "", "  ")
	if err != nil {
		return Identity{}, err
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		return Identity{}, err
	}
	log(fmt.Sprintf("  identity: %s / %s (written to %s)", ident.Serial, ident.MAC, path))
	return ident, nil
}

// generateSerial runs macserial, which knows Apple's checksums. See macserialMember.
func generateSerial(toolDir, model string) (string, string, error) {
	tool := filepath.Join(toolDir, "macserial")
	if _, err := os.Stat(tool); err != nil {
		return "", "", fmt.Errorf("macserial is not at %s: it ships inside OpenCore's archive "+
			"under %s, and unpacking the archive puts it there", tool, macserialMember)
	}
	out, err := exec.Command(tool, "--num", "1", "--model", model).Output()
	if err != nil {
		return "", "", fmt.Errorf("macserial failed: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	line := lines[len(lines)-1]
	parts := strings.SplitN(line, "|", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("macserial printed %q, which has no '|' separator", line)
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}

// generateMAC produces an address with Apple's own OUI prefix.
//
// MACOS READS THE PREFIX: 00:16:CB is Apple's, and a guest with a random locally administered
// address is a differently-behaved machine.
func generateMAC() string {
	tail := make([]byte, 3)
	for i := range tail {
		tail[i] = byte(rand.New(rand.NewSource(time.Now().UnixNano() + int64(i))).Intn(256))
	}
	return fmt.Sprintf("00:16:CB:%02X:%02X:%02X", tail[0], tail[1], tail[2])
}

// newUUID makes a random version-4 UUID in the upper-case form Apple's tools print.
func newUUID() string {
	b := make([]byte, 16)
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	for i := range b {
		b[i] = byte(r.Intn(256))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%X-%X-%X-%X-%X", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func copyTree(from, to string) error {
	return filepath.Walk(from, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
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
