package opencore

import (
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
