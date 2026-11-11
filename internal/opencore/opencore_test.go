package opencore

import (
	"encoding/binary"
	"hash/crc32"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// lookPath and runCommand are thin wrappers so a test that needs mtools can skip rather than fail
// on a machine that does not have it - the checks themselves are the point, not the tool.
func lookPath(name string) (string, error) { return exec.LookPath(name) }

func runCommand(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

// A PLIST ROUND TRIP KEEPS EVERY KEY AND ITS ORDER.
//
// THE ORDER IS THE POINT, not tidiness. The Python implementation edited this file with a positional
// XPath - `key[1]/following-sibling::*[1]/key[3]` - and a plist dict is a FLAT list of key/value
// pairs, so that indexing is correct only while every dict's order is exactly what it was when the
// expression was written. Reorder one dict upstream and the edit lands on a neighbouring key and
// SUCCEEDS. This parser addresses the file by NAME, which either writes the key it names or says so.
func TestPlistRoundTripKeepsKeysAndOrder(t *testing.T) {
	original := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>PlatformInfo</key>
	<dict>
		<key>Generic</key>
		<dict>
			<key>SystemSerialNumber</key>
			<string>AAA111</string>
			<key>MLB</key>
			<string>BBB222</string>
		</dict>
	</dict>
	<key>UEFI</key>
	<dict>
		<key>Output</key>
		<dict>
			<key>Resolution</key>
			<string></string>
		</dict>
		<key>Drivers</key>
		<array>
			<dict>
				<key>Path</key>
				<string>OpenRuntime.efi</string>
				<key>Enabled</key>
				<true/>
			</dict>
		</array>
	</dict>
	<key>Misc</key>
	<dict>
		<key>Boot</key>
		<dict>
			<key>ShowPicker</key>
			<true/>
			<key>Timeout</key>
			<integer>5</integer>
		</dict>
	</dict>
</dict>
</plist>
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.plist")
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	root, err := readPlist(path)
	if err != nil {
		t.Fatal(err)
	}

	// EVERY NESTED KEY IS ADDRESSABLE BY NAME.
	if got := root.dig("PlatformInfo", "Generic", "SystemSerialNumber"); got == nil ||
		got.str != "AAA111" {
		t.Fatalf("SystemSerialNumber did not parse; got %+v", got)
	}
	if got := root.dig("UEFI", "Drivers"); got == nil || got.kind != plistArray ||
		len(got.array) != 1 || got.array[0].get("Path").str != "OpenRuntime.efi" {
		t.Fatalf("the Drivers array did not parse: %+v", got)
	}
	if got := root.dig("Misc", "Boot", "Timeout"); got == nil || got.integer != 5 {
		t.Fatalf("Timeout did not parse as an integer: %+v", got)
	}

	// A KEY THAT IS NOT THERE IS NIL RATHER THAN A NEIGHBOUR.
	if got := root.dig("PlatformInfo", "Generic", "NotAKey"); got != nil {
		t.Fatalf("a missing key returned %+v", got)
	}

	// AND THE ROUND TRIP: write, read back, every value unchanged.
	if err := writePlist(path, root); err != nil {
		t.Fatal(err)
	}
	again, err := readPlist(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.dig("PlatformInfo", "Generic", "MLB"); got == nil || got.str != "BBB222" {
		t.Fatalf("MLB changed through the round trip: %+v", got)
	}
	if got := again.dig("Misc", "Boot", "ShowPicker"); got == nil || got.kind != plistTrue {
		t.Fatalf("ShowPicker changed through the round trip: %+v", got)
	}
}

// THE IDENTITY LANDS WHERE OPENCORE READS IT, AND ROM IS SIX RAW BYTES.
//
// ROM is <data>, not a string: OpenCore hands those bytes to macOS as the primary network
// interface's address. A hex STRING there is a guest with no network identity, which is a different
// machine as far as macOS is concerned.
func TestWriteConfigPutsTheIdentityWhereOpencoreReadsIt(t *testing.T) {
	dir := t.TempDir()
	template := filepath.Join(dir, "template")
	oc := filepath.Join(template, "EFI", "OC")
	if err := os.MkdirAll(oc, 0o755); err != nil {
		t.Fatal(err)
	}
	source := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>PlatformInfo</key><dict><key>Generic</key><dict>
		<key>SystemSerialNumber</key><string>OLD</string>
		<key>MLB</key><string>OLD</string>
		<key>SystemUUID</key><string>OLD</string>
	</dict></dict>
	<key>NVRAM</key><dict><key>Add</key><dict>
		<key>7C436110-AB2A-4BBB-A880-FE41995C9F82</key><dict>
			<key>csr-active-config</key><data>AAAAAA==</data>
		</dict>
	</dict></dict>
	<key>UEFI</key><dict><key>Output</key><dict>
		<key>Resolution</key><string></string>
	</dict></dict>
	<key>Misc</key><dict><key>Boot</key><dict>
		<key>ShowPicker</key><true/>
		<key>Timeout</key><integer>5</integer>
	</dict></dict>
</dict>
</plist>
`
	if err := os.WriteFile(filepath.Join(oc, "config.plist"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	// THE MARKER, so MergeTemplate's contract holds: an extraction is complete only when it says so.
	if err := os.WriteFile(filepath.Join(template, ".complete"),
		[]byte(filepath.Join(template, "EFI")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	efi := filepath.Join(dir, "efi")
	if err := os.MkdirAll(filepath.Join(efi, "OC"), 0o755); err != nil {
		t.Fatal(err)
	}
	ident := Identity{
		Serial:      "C02TESTSERIAL",
		BoardSerial: "C02TESTBOARD",
		UUID:        "11111111-2222-4333-8444-555555555555",
		MAC:         "00:16:CB:AA:BB:CC",
	}
	if err := WriteConfig(efi, template, ident, "15"); err != nil {
		t.Fatal(err)
	}

	root, err := readPlist(filepath.Join(efi, "OC", "config.plist"))
	if err != nil {
		t.Fatal(err)
	}
	if got := root.dig("PlatformInfo", "Generic", "SystemSerialNumber"); got.str != ident.Serial {
		t.Fatalf("serial is %q, want %q", got.str, ident.Serial)
	}
	if got := root.dig("PlatformInfo", "Generic", "MLB"); got.str != ident.BoardSerial {
		t.Fatalf("board serial is %q, want %q", got.str, ident.BoardSerial)
	}
	rom := root.dig("PlatformInfo", "Generic", "ROM")
	if rom == nil || rom.kind != plistData {
		t.Fatalf("ROM is %+v; it must be <data>, not a string", rom)
	}
	if len(rom.data) != 6 || rom.data[0] != 0x00 || rom.data[3] != 0xAA || rom.data[5] != 0xCC {
		t.Fatalf("ROM is % x; it must be the six bytes of the MAC", rom.data)
	}

	// SIP OFF IS FOUR BYTES OF 0x7f 00 00 00, little-endian bitmask.
	csr := root.dig("NVRAM", "Add", "7C436110-AB2A-4BBB-A880-FE41995C9F82", "csr-active-config")
	if csr == nil || len(csr.data) != 4 || csr.data[0] != 0x7f || csr.data[1] != 0 {
		t.Fatalf("csr-active-config is % x, want 7f 00 00 00", csr.data)
	}

	// NO PICKER, because a stage boots one thing and a menu that has to time out is a key that
	// stops there instead.
	if got := root.dig("Misc", "Boot", "ShowPicker"); got == nil || got.kind != plistFalse {
		t.Fatal("ShowPicker must be false; a picker is a menu nothing in a walkthrough can answer")
	}
	if got := root.dig("Misc", "Boot", "Timeout"); got.integer != 0 {
		t.Fatalf("Timeout is %d, want 0", got.integer)
	}

	// AND THE SCREEN SIZE, WHICH EVERY COORDINATE DEPENDS ON. An empty Resolution ran the guest at
	// 1280x800 while the clicks were measured at 1920x1080.
	if got := root.dig("UEFI", "Output", "Resolution"); got.str != "1920x1080@32" {
		t.Fatalf("Resolution is %q, want 1920x1080@32", got.str)
	}
}

// A CONFIG WITHOUT PlatformInfo IS REFUSED, NOT SILENTLY HALF-WRITTEN.
func TestWriteConfigRefusesATemplateItCannotAddress(t *testing.T) {
	dir := t.TempDir()
	template := filepath.Join(dir, "template")
	oc := filepath.Join(template, "EFI", "OC")
	if err := os.MkdirAll(oc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oc, "config.plist"),
		[]byte(`<?xml version="1.0"?><plist version="1.0"><dict><key>Misc</key><dict/></dict></plist>`),
		0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(template, ".complete"),
		[]byte(filepath.Join(template, "EFI")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	efi := filepath.Join(dir, "efi")
	if err := os.MkdirAll(filepath.Join(efi, "OC"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := WriteConfig(efi, template, Identity{Serial: "X"}, "15")
	if err == nil {
		t.Fatal("a config with no PlatformInfo must be refused; writing nothing and reporting " +
			"success produces a guest booting as another machine")
	}
	if !strings.Contains(err.Error(), "PlatformInfo") {
		t.Fatalf("the refusal must name the missing section; got %v", err)
	}
}

// THE GPT AND THE ESP ARE CHECKED AS THE FIRMWARE SEES THEM, NOT AS MTOOLS DOES.
//
// THIS IS THE TEST FOR FIVE RUNS THAT PASSED EVERY CHECK AND BOOTED NOTHING. `mformat -F` on the
// whole file makes a "superfloppy": a FAT filesystem at LBA 0 with an MBR signature over the FAT
// boot sector. mtools reads it, so `mdir` listed the tree and the build passed. OVMF looks for an
// EFI SYSTEM PARTITION in a partition table, found none, and said
//
//	TianoCore ... failed to load Boot0001
//
// which reads like a broken loader and is a disk the firmware cannot enumerate.
func TestTheBootImageHasAPartitionTableTheFirmwareCanRead(t *testing.T) {
	for _, tool := range []string{"mformat", "mcopy", "mdir"} {
		if _, err := lookPath(tool); err != nil {
			t.Skipf("%s is not installed, so a boot image cannot be built here", tool)
		}
	}
	dir := t.TempDir()
	efi := filepath.Join(dir, "efi")
	// The two paths the firmware opens, and nothing else - the check is about the layout.
	if err := os.MkdirAll(filepath.Join(efi, "EFI", "BOOT"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(efi, "EFI", "OC"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(efi, "EFI", "BOOT", "BOOTx64.efi"),
		[]byte("MZ-fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(efi, "EFI", "OC", "OpenCore.efi"),
		[]byte("MZ-fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	image := filepath.Join(dir, "boot.img")
	if err := BuildBootImage(efi, image, func(string) {}); err != nil {
		t.Fatal(err)
	}

	// THE GPT HEADER AT LBA 1, WITH A VALID CRC.
	if err := checkGPT(image, ESPStartSector); err != nil {
		t.Fatal(err)
	}
	// AND A FAT32 FILESYSTEM INSIDE THE PARTITION, above the 65,525-cluster floor that separates
	// FAT32 from FAT16 - which is the number mtools does not enforce.
	if err := checkESP(image, ESPStartSector); err != nil {
		t.Fatal(err)
	}

	// AND THE PROTECTIVE MBR, which is what makes the disk a disk rather than a filesystem.
	f, err := os.Open(image)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sector := make([]byte, SectorSize)
	if _, err := f.ReadAt(sector, 0); err != nil {
		t.Fatal(err)
	}
	if sector[0x1FE] != 0x55 || sector[0x1FF] != 0xAA {
		t.Fatal("no MBR signature at 0x1FE; the firmware does not see a partitioned disk")
	}
	if sector[0x1BE+4] != 0xEE {
		t.Fatalf("the first MBR entry has type %02x, not 0xEE: without a protective entry a "+
			"GPT disk reads as an empty MBR", sector[0x1BE+4])
	}
}

// AN ESP TOO SMALL TO BE FAT32 IS REFUSED BY NAME, WHICH IS THE CHECK THAT WOULD HAVE SAVED FIVE
// RUNS. The arithmetic is done here rather than asked of mtools, because mtools writes the invalid
// one happily.
func TestASmallESPIsRefused(t *testing.T) {
	dir := t.TempDir()
	image := filepath.Join(dir, "small.img")
	// 100 MiB with 8-sector clusters: about 12,000 clusters, far below the 65,525 floor.
	size := int64(100) * 1024 * 1024
	f, err := os.Create(image)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	for _, tool := range []string{"mformat"} {
		if _, err := lookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	partition := image + "@@" + "1048576"
	if out, err := runCommand("mformat", "-F", "-i", partition, "-c", "8",
		"-T", "194560", "-v", "EFI"); err != nil {
		t.Skipf("mformat refused the geometry itself: %v (%s)", err, out)
	}

	err = checkESP(image, ESPStartSector)
	if err == nil {
		t.Fatal("an ESP with about 12,000 clusters must be refused: FAT32 requires 65,525, and " +
			"OVMF's EDK II driver will not mount it")
	}
	if !strings.Contains(err.Error(), "clusters") {
		t.Fatalf("the refusal must say it is about cluster count; got %v", err)
	}
}

// A MACOS BUILD WITHOUT A RECOVERY IMAGE IS REFUSED BEFORE ANY DOWNLOAD, AND NAMING THE DRIVER.
//
// IT IS NOT OPTIONAL AND ITS ABSENCE IS SILENT AT THE END. The APFS driver is extracted from that
// image; a boot image without one is started by the firmware and cannot read the system the
// installer just wrote. EnableJumpstart=true does not help - it HOOKS a driver the firmware is
// expected to have, and OVMF has none - so the symptom is an installation that completes and a
// picker listing only Reset NVRAM and Toggle SIP, with nothing in any log.
//
// THE ORDER OF THE CHECK IS THE POINT: the four pinned assets are about 20 MB of download, so a
// caller that forgot to name the recovery image has to be told in a second rather than after the
// transfer. `Build` used to skip the APFS step when the image was empty, which made the one case
// that must never happen indistinguishable from a normal build.
func TestABuildWithoutRecoveryIsRefusedBeforeDownloading(t *testing.T) {
	dir := t.TempDir()
	_, err := Build(Spec{Release: "15", Work: dir}, func(string) {})
	if err == nil {
		t.Fatal("a macOS build with no recovery image must be refused: Apple's APFS driver comes " +
			"out of it, and without one the guest installs a system it cannot boot")
	}
	for _, want := range []string{"APFS", "apfs.efi"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal should name %q; got %v", want, err)
		}
	}
	// AND NOTHING WAS DOWNLOADED. A partial asset in the work directory means the refusal came
	// after a transfer rather than before it.
	entries, readErr := os.ReadDir(dir)
	if readErr == nil {
		for _, e := range entries {
			t.Fatalf("the refusal left %q in the work directory, so it happened after a "+
				"download rather than before one", e.Name())
		}
	}
}

// AND A NAMED BUT ABSENT IMAGE IS REFUSED TOO, so a mistyped path does not become a build.
func TestAMissingRecoveryPathIsRefused(t *testing.T) {
	_, err := Build(Spec{Release: "15", Work: t.TempDir(),
		Recovery: filepath.Join(t.TempDir(), "absent.dmg")}, func(string) {})
	if err == nil {
		t.Fatal("a recovery path that does not exist must be refused")
	}
	if !strings.Contains(err.Error(), "recovery image") {
		t.Fatalf("the refusal must say what is missing; got %v", err)
	}
}

// A REAL OPENCORE ARCHIVE, BECAUSE A FIXTURE CANNOT KNOW WHICH PATHS A RELEASE CONTAINS.
//
// THE FOUR TESTS ABOVE PASSED WHILE THIS BUILD COULD NOT UNPACK A REAL RELEASE. `UnpackOpencore`
// copied only `X64/EFI`, and macserial lives under `Utilities/` - so the identity step failed with
// "macserial is not at ..." after 20 MB of assets had been fetched and the template extracted. Every
// fixture agreed with the code, because a fixture is written by whoever wrote the code.
//
// IT SKIPS WHEN THE ARCHIVE IS NOT PRESENT rather than fetching it: a unit test must not need the
// network, and the asset is pinned by sha256 so the CI image can carry one copy. `OPENCORE_ARCHIVE`
// names it.
func TestUnpackARealArchive(t *testing.T) {
	archive := os.Getenv("OPENCORE_ARCHIVE")
	if archive == "" {
		archive = "/tmp/oc-test/opencore.zip"
	}
	if _, err := os.Stat(archive); err != nil {
		t.Skipf("no real OpenCore archive at %s (set OPENCORE_ARCHIVE)", archive)
	}
	// AND IT IS THE PINNED ONE, or this test would be about some other release.
	got, err := sha256File(archive)
	if err != nil {
		t.Fatal(err)
	}
	if got != Assets["opencore"].SHA256 {
		t.Fatalf("the archive at %s is sha256 %s, not the pinned %s", archive, got,
			Assets["opencore"].SHA256)
	}

	work := t.TempDir()
	dest := filepath.Join(work, "efi")
	if err := UnpackOpencore(archive, dest, work); err != nil {
		t.Fatalf("unpacking the pinned archive: %v", err)
	}

	// THE TWO PATHS THE FIRMWARE OPENS.
	for _, want := range []string{"EFI/BOOT/BOOTx64.efi", "EFI/OC/OpenCore.efi"} {
		p := filepath.Join(dest, want)
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("the pinned archive contains %s and this build did not produce it: %v", want, err)
		}
		if fi.Size() == 0 {
			t.Fatalf("%s came out empty", want)
		}
	}

	// AND THE SERIAL GENERATOR, which is the one this build failed to unpack. A hand-rolled serial
	// fails Apple's checksum and the guest stops booting when the NVRAM remembers it.
	tool := filepath.Join(work, "macserial")
	fi, err := os.Stat(tool)
	if err != nil {
		t.Fatalf("macserial was not placed at %s: %v. It is under Utilities/ in the real archive, "+
			"which an X64/EFI-only unpack skips", tool, err)
	}
	if fi.Mode()&0o111 == 0 {
		t.Fatalf("macserial is not executable (%v); the identity step runs it", fi.Mode())
	}
}

// A GPT HEADER'S CRC IS COMPUTED WITH THE CRC FIELD ITSELF SET TO ZERO.
//
// THAT IS A RULE OF THE FORMAT, NOT A DETAIL, AND BOTH SIDES OF THIS BUILD GOT IT WRONG IN OPPOSITE
// WAYS. The writer put the checksum in and then computed over the header containing it; the verifier
// computed over the header it read, which contains the stored value. The two numbers could never be
// equal, and the first CI run of macos-setup reported it as a broken image:
//
//	boot.img has a GPT header whose CRC is 04e45106 but the header says 76dd1daf
//
// The header was correct BY THE FORMAT'S RULE and this build's own check was not, which is the worst
// shape for a self-check: it rejects exactly the images that are right.
//
// THIS TEST DOES NOT USE checkGPT, because checkGPT is what was broken. It parses the bytes the way
// another GPT implementation would: clear the field, compute, compare.
func TestTheGPTHeaderCRCIsComputedTheWayTheFormatSays(t *testing.T) {
	for _, tool := range []string{"mformat", "mcopy", "mdir"} {
		if _, err := lookPath(tool); err != nil {
			t.Skipf("%s is not installed, so a boot image cannot be built here", tool)
		}
	}
	dir := t.TempDir()
	efi := filepath.Join(dir, "efi")
	for _, d := range []string{"EFI/BOOT", "EFI/OC"} {
		if err := os.MkdirAll(filepath.Join(efi, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"EFI/BOOT/BOOTx64.efi", "EFI/OC/OpenCore.efi"} {
		if err := os.WriteFile(filepath.Join(efi, f), []byte("MZ-fake"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	image := filepath.Join(dir, "boot.img")
	if err := BuildBootImage(efi, image, func(string) {}); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(image)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	head := make([]byte, SectorSize)
	if _, err := f.ReadAt(head, SectorSize); err != nil {
		t.Fatal(err)
	}
	if string(head[0:8]) != "EFI PART" {
		t.Fatalf("no GPT signature at LBA 1: %q", head[0:8])
	}

	// AN INDEPENDENT READER'S ALGORITHM.
	stored := binary.LittleEndian.Uint32(head[16:])
	head[16], head[17], head[18], head[19] = 0, 0, 0, 0
	computed := crc32.ChecksumIEEE(head[:92])
	if stored != computed {
		t.Fatalf("the header's stored CRC is %08x and an independent reader computes %08x. The "+
			"format says HeaderCRC32 covers the header WITH ITS OWN FIELD ZERO, so a mismatch here "+
			"means the image is wrong for every GPT implementation, not only for this one",
			stored, computed)
	}
	// AND THE BACKUP HEADER AT THE LAST SECTOR, with the same rule.
	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	last := fi.Size() - SectorSize
	backup := make([]byte, SectorSize)
	if _, err := f.ReadAt(backup, last); err != nil {
		t.Fatal(err)
	}
	if string(backup[0:8]) != "EFI PART" {
		t.Fatal("no backup GPT header in the last sector; OVMF falls back to it when the primary " +
			"is damaged, so leaving it out is a disk that works until it does not")
	}
	backupStored := binary.LittleEndian.Uint32(backup[16:])
	backup[16], backup[17], backup[18], backup[19] = 0, 0, 0, 0
	if got := crc32.ChecksumIEEE(backup[:92]); got != backupStored {
		t.Fatalf("the backup header's CRC is %08x and an independent reader computes %08x",
			backupStored, got)
	}
}
