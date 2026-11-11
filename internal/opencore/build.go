package opencore

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// --- the template ---------------------------------------------------------------------

// ExtractTemplateBoot returns the EFI tree from the LongQT template image.
//
// THE TEMPLATE SUPPLIES WHAT OPENCORE'S ARCHIVE DOES NOT: Lilu, VirtualSMC, WhateverGreen, the
// VoodooPS2 input drivers, the ACPI tables and upstream's config.plist. OpenCore's own archive is
// the bootloader and nothing else, so a guest booted from it alone stops at a prohibited sign with
// an empty log.
//
// 7Z AND NOT AN ISO READER, because the image carries two name tables: the ISO9660 one is 8.3
// uppercase with a `;1` suffix (`CONFIG.PLIST;1`, `SSDT_EC.AML`) and the Joliet one has the names
// the config refers to (`config.plist`, `Lilu.kext`). Extracting through the 9660 table produces a
// tree whose names match nothing OpenCore looks for.
//
// THE RESULT IS THE `EFI` DIRECTORY - not the `OC` one and not the extraction root - because the
// caller merges its contents over OpenCore's, so BOOT/ and OC/ must sit at its top level.
func ExtractTemplateBoot(templateISO, dest string) error {
	sevenZip, err := exec.LookPath("7z")
	if err != nil {
		return fmt.Errorf("7z is not installed: %w", err)
	}
	// A CACHE HIT MUST PROVE A COMPLETE EXTRACTION, NOT A DIRECTORY.
	//
	// The Python implementation returned the extraction root whenever the directory existed, and
	// an interrupted run left it present and EMPTY - so the next call returned a root with no
	// OC/Kexts under it and reported "the template supplied no OC/Kexts" about a template that was
	// perfectly good. The marker is written only after every check below passes.
	marker := filepath.Join(dest, ".complete")
	if body, err := os.ReadFile(marker); err == nil {
		cached := strings.TrimSpace(string(body))
		if cached != "" && dirExists(filepath.Join(cached, "OC")) {
			return nil
		}
		_ = os.RemoveAll(dest)
	}
	if _, err := os.Stat(dest); err == nil {
		_ = os.RemoveAll(dest)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}

	out := filepath.Join(dest, "extract")
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	cmd := exec.Command(sevenZip, "x", "-y", "-o"+out, templateISO)
	if o, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("unpacking %s: %v (%s)", filepath.Base(templateISO), err,
			firstLine(string(o)))
	}

	// THE RELEASE TREE, CHOSEN BY NAME. The image holds EFI_RELEASE and EFI_DEBUG; the debug build
	// is slower and writes a log to the ESP that nothing here reads. Choosing by name rather than
	// taking the first match matters because a directory walk returns whatever order the filesystem
	// gives.
	chosen, err := findEFITree(out)
	if err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(templateISO), err)
	}
	for _, required := range []string{
		filepath.Join("OC", "ACPI"), filepath.Join("OC", "Kexts"),
		filepath.Join("OC", "Drivers"), filepath.Join("OC", "Resources"),
	} {
		if !dirExists(filepath.Join(chosen, required)) {
			return fmt.Errorf("%s has no %s under its EFI tree; the guest would boot to a "+
				"prohibited sign with nothing in any log to explain it", templateISO,
				required)
		}
	}
	if !fileExists(filepath.Join(chosen, "OC", "config.plist")) {
		return fmt.Errorf("%s has no OC/config.plist; the template layout changed",
			templateISO)
	}
	if err := os.WriteFile(marker, []byte(chosen+"\n"), 0o644); err != nil {
		return err
	}
	return nil
}

// findEFITree locates the EFI directory that has an OC/OpenCore.efi beneath it.
func findEFITree(root string) (string, error) {
	var candidates []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || !info.IsDir() {
			return err
		}
		if !strings.EqualFold(info.Name(), "OC") {
			return nil
		}
		if !fileExists(filepath.Join(path, "OpenCore.efi")) {
			return nil
		}
		parent := filepath.Dir(path)
		if strings.EqualFold(filepath.Base(parent), "EFI") {
			candidates = append(candidates, parent)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("holds no EFI/OC/OpenCore.efi; the template layout changed")
	}
	for _, c := range candidates {
		if strings.Contains(strings.ToUpper(c), "EFI_RELEASE") {
			return c, nil
		}
	}
	return candidates[0], nil
}

// templateEFIPath is where ExtractTemplateBoot leaves the chosen tree, for a caller that needs it.
func templateEFIPath(dest string) string {
	body, err := os.ReadFile(filepath.Join(dest, ".complete"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(body))
}

// MergeTemplate copies the template's OC subtree over OpenCore's, keeping OpenCore's binaries.
//
// THE TEMPLATE CARRIES THE CONFIG, THE KEXTS, THE ACPI TABLES AND THE DRIVERS; OPENCORE'S ARCHIVE
// CARRIES THE BOOTLOADER. Neither is complete, and the merge is what makes one tree that is.
// OpenCore's own Config.plist is NOT copied - it ships under Docs/Sample.plist rather than in
// X64/EFI, which is why the template's is the only one in the result.
func MergeTemplate(efiDir, templateDir string) (string, error) {
	tree := templateEFIPath(templateDir)
	if tree == "" {
		return "", fmt.Errorf("the template extraction is not marked complete; " +
			"ExtractTemplateBoot must succeed before the merge")
	}
	// THE BOOTLOADER STAYS OPENCORE'S. Copying the template's over it would work today and break
	// the day the two releases differ - and the pin is on OpenCore's archive, so that is the one
	// whose version this build claims.
	if err := copyTree(tree, efiDir); err != nil {
		return "", err
	}
	if !fileExists(filepath.Join(efiDir, "OC", "OpenCore.efi")) ||
		!fileExists(filepath.Join(efiDir, "EFI", "BOOT", "BOOTx64.efi")) {
		return "", fmt.Errorf("the merged tree at %s is missing the bootloader: the firmware "+
			"opens \\EFI\\BOOT\\BOOTX64.EFI and nothing else", efiDir)
	}
	return efiDir, nil
}

// --- config.plist ---------------------------------------------------------------------

// WriteConfig merges the identity into the template's config.plist.
//
// ONLY THE FIELDS THIS BUILD DEPENDS ON ARE CHANGED. The template's config carries device properties
// and kext lists that were arrived at by somebody else's runs - the GPU and SMC properties in
// particular are not guessable - so replacing the file would throw those away.
func WriteConfig(efiDir, templateDir string, ident Identity, release string) error {
	source := findFileCI(filepath.Join(templateEFIPath(templateDir), "OC"), "config.plist")
	if source == "" {
		return fmt.Errorf("the template has no config.plist under its OC directory; the " +
			"layout changed")
	}
	root, err := readPlist(source)
	if err != nil {
		return fmt.Errorf("%s: %w", source, err)
	}

	// THE IDENTITY. ROM is six raw bytes of the MAC as <data>, not a hex string - OpenCore hands
	// those bytes to macOS as the primary network interface's address.
	generic := root.dig("PlatformInfo", "Generic")
	if generic == nil {
		return fmt.Errorf("the template's config has no PlatformInfo/Generic; the layout changed")
	}
	generic.set("SystemSerialNumber", newString(ident.Serial))
	generic.set("MLB", newString(ident.BoardSerial))
	generic.set("SystemUUID", newString(ident.UUID))
	generic.set("ROM", &plistValue{kind: plistData, data: macBytes(ident.MAC)})

	// SIP OFF. A 4-byte little-endian bitmask, and 0x7f disables every protection - which the
	// sandbox image needs for its TCC and xcode work.
	if nvram := root.dig("NVRAM", "Add", "7C436110-AB2A-4BBB-A880-FE41995C9F82"); nvram != nil {
		nvram.set("csr-active-config", &plistValue{kind: plistData, data: []byte{0x7f, 0, 0, 0}})
	}

	// NO PICKER. A stage boots one thing: the installer. OpenCore's picker with a two-second
	// timeout shows a menu that has to time out before anything happens - and if a key arrives
	// while it is up, it stops there instead.
	if bootCfg := root.dig("Misc", "Boot"); bootCfg != nil {
		bootCfg.set("ShowPicker", newBool(false))
		bootCfg.set("Timeout", newInteger(0))
	}

	// THE SCREEN SIZE IS NOT DECORATION - EVERY COORDINATE IN A WALKTHROUGH DEPENDS ON IT.
	//
	// The template ships Resolution EMPTY, and an empty one made the guest run at 1280x800 while
	// every click in macos/15/steps was measured at 1920x1080. The licence pane's Agree at y=810
	// was below the bottom of an 800-pixel screen, and six runs clicked at coordinates that were
	// never on screen.
	//
	// RESOLUTION ONLY. ForceResolution is left alone, because 1920x1080 is what the pinned
	// OVMF_VARS template was built for - the GOP mode is baked into it - so this is a matched pair
	// rather than one setting being imposed.
	if output := root.dig("UEFI", "Output"); output != nil {
		output.set("Resolution", newString(fmt.Sprintf("%dx%d@32", GuestWidth, GuestHeight)))
	}

	target := filepath.Join(efiDir, "OC", "config.plist")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := writePlist(target, root); err != nil {
		return err
	}

	// READ BACK RATHER THAN TRUSTING THE WRITE. This file is the whole difference between a guest
	// that boots and one that stops at a prohibited sign, and a serial that did not land is a guest
	// booting as another machine.
	check, err := readPlist(target)
	if err != nil {
		return fmt.Errorf("%s is not readable after writing: %w", target, err)
	}
	if got := check.dig("PlatformInfo", "Generic", "SystemSerialNumber"); got == nil ||
		got.str != ident.Serial {
		return fmt.Errorf("%s does not hold the serial after writing; the identity did not "+
			"land and the guest would boot with another machine's", target)
	}
	return nil
}

// macBytes turns "00:16:CB:AA:BB:CC" into the six bytes OpenCore wants.
func macBytes(mac string) []byte {
	parts := strings.Split(strings.ReplaceAll(mac, "-", ":"), ":")
	out := make([]byte, 0, len(parts))
	for _, p := range parts {
		var b byte
		_, err := fmt.Sscanf(p, "%02x", &b)
		if err == nil {
			out = append(out, b)
		}
	}
	return out
}

// --- the APFS driver ------------------------------------------------------------------

// InstallAPFSDriver puts Apple's own APFS driver into the boot image and registers it.
//
// WHY THIS IS NOT OPTIONAL, AND WHY IT IS NOT IN THE PINS. OpenCore ships no APFS driver and neither
// does the template. Its config carries EnableJumpstart=true, which is often mistaken for a driver:
// it is not - Jumpstart HOOKS a driver the firmware is expected to already have, and OVMF has none.
//
// THE SYMPTOM IS SPECIFIC AND SILENT: an installation completes, the disk is full of a working
// macOS, and OpenCore's picker lists only Reset NVRAM and Toggle SIP because it cannot read the APFS
// partition the system is on. Nothing in any log says "no APFS driver".
//
// THE DRIVER IS ALREADY ON DISK, INSIDE THE RECOVERY IMAGE. macOS ships it at
// usr/standalone/i386/apfs.efi in BaseSystem, which is what the installer itself uses. Copying it
// off the INSTALLED disk is impossible before the first boot - which is why dockur's
// fix-opencore-apfs.sh has to do exactly that on a second run - and taking it from a Linux package
// would be apfs-fuse's userspace library, not Apple's driver.
func InstallAPFSDriver(efiDir, recovery, work string, log func(string)) error {
	source := filepath.Join(work, "apfs.efi")
	if !fileExists(source) {
		if !fileExists(recovery) {
			return fmt.Errorf("no recovery image at %s, and the APFS driver can only come from "+
				"one; the guest would install a macOS it cannot boot", recovery)
		}
		log(fmt.Sprintf("  extracting apfs.efi from %s", filepath.Base(recovery)))
		sevenZip, err := exec.LookPath("7z")
		if err != nil {
			return fmt.Errorf("7z is not installed: %w", err)
		}
		extract := filepath.Join(work, "apfs-extract")
		_ = os.RemoveAll(extract)
		// 7z walks the DMG's compressed blocks and its HFS+ contents, which is the only way to
		// reach a file inside a UDIF image without mounting it.
		cmd := exec.Command(sevenZip, "x", "-y", "-o"+extract, recovery, apfsDriverPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("extracting %s from %s: %v (%s)", apfsDriverPath,
				filepath.Base(recovery), err, firstLine(string(out)))
		}
		found := ""
		_ = filepath.Walk(extract, func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() &&
				strings.EqualFold(info.Name(), filepath.Base(apfsDriverPath)) && found == "" {
				found = path
			}
			return nil
		})
		if found == "" {
			return fmt.Errorf("%s holds no %s; the recovery layout changed. Without it OpenCore "+
				"cannot read the installed system, and the result is a disk that only boots with "+
				"a matching recovery attached", recovery, apfsDriverPath)
		}
		if err := os.Rename(found, source); err != nil {
			if err := copyFile(found, source); err != nil {
				return err
			}
		}
	}

	// A DRIVER THAT IS NOT A PE IMAGE IS NOT A DRIVER. Cheap, and it is the difference between
	// "the extraction produced a file" and "the extraction produced the file" - an obfuscated or
	// truncated apfs.efi fails at boot with no message at all.
	head := make([]byte, 2)
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	_, err = f.Read(head)
	_ = f.Close()
	if err != nil {
		return err
	}
	if string(head) != "MZ" {
		return fmt.Errorf("%s is not a PE image (begins %q, expected \"MZ\")", source, head)
	}
	fi, _ := os.Stat(source)

	drivers := filepath.Join(efiDir, "OC", "Drivers")
	if err := os.MkdirAll(drivers, 0o755); err != nil {
		return err
	}
	if err := copyFile(source, filepath.Join(drivers, "apfs.efi")); err != nil {
		return err
	}

	configPath := findFileCI(filepath.Join(efiDir, "OC"), "config.plist")
	if configPath == "" {
		return fmt.Errorf("no config.plist under %s/OC to register apfs.efi in", efiDir)
	}
	root, err := readPlist(configPath)
	if err != nil {
		return err
	}
	ufi := root.dig("UEFI")
	if ufi == nil {
		return fmt.Errorf("the config has no UEFI section; apfs.efi cannot be registered")
	}
	list := ufi.get("Drivers")
	if list == nil || list.kind != plistArray {
		list = &plistValue{kind: plistArray}
		ufi.set("Drivers", list)
	}
	already := false
	for _, entry := range list.array {
		if p := entry.get("Path"); p != nil && strings.EqualFold(p.str, "apfs.efi") {
			already = true
		}
	}
	if !already {
		// APPENDED RATHER THAN PREPENDED: the drivers OpenCore loads first are the ones it needs
		// for the filesystem it is about to read - this one - so it goes last.
		entry := newDict()
		entry.set("Arguments", newString(""))
		entry.set("Comment", newString("Apple's APFS driver, from the recovery image"))
		entry.set("Enabled", newBool(true))
		// LoadEarly=false: it is a filesystem driver, and OpenCore's documentation puts those
		// after the runtime services drivers (OpenRuntime) that they may call into.
		entry.set("LoadEarly", newBool(false))
		entry.set("Path", newString("apfs.efi"))
		list.array = append(list.array, entry)
		if err := writePlist(configPath, root); err != nil {
			return err
		}
	}

	check, err := readPlist(configPath)
	if err != nil {
		return err
	}
	enabled := false
	if list := check.dig("UEFI", "Drivers"); list != nil {
		for _, entry := range list.array {
			p := entry.get("Path")
			e := entry.get("Enabled")
			if p != nil && strings.EqualFold(p.str, "apfs.efi") &&
				e != nil && e.kind == plistTrue {
				enabled = true
			}
		}
	}
	if !enabled {
		return fmt.Errorf("apfs.efi did not land in %s as an enabled driver", configPath)
	}
	log(fmt.Sprintf("  apfs.efi: %d KiB, registered in config.plist", fi.Size()>>10))
	return nil
}

// --- the boot image -------------------------------------------------------------------

// BuildBootImage writes a GPT disk whose single partition is the ESP OpenCore boots from.
//
// THE PARTITION TABLE IS THE POINT, AND ITS ABSENCE WAS A BUG THAT COST FIVE RUNS. The first version
// ran `mformat -F` on the whole file, which makes a "superfloppy": a FAT filesystem starting at LBA 0
// with an MBR signature written over the FAT boot sector. mtools reads it happily, so every check on
// this side passed. OVMF did not - firmware looks for an EFI SYSTEM PARTITION in a GPT or MBR
// partition table, and a filesystem at LBA 0 is neither - and the run died at
//
//	TianoCore ... failed to load Boot0001
//
// which reads like a broken loader and is actually a disk the firmware cannot enumerate.
//
// WHAT A BOOTABLE ESP NEEDS, all of it written here:
//
//	LBA 0        a PROTECTIVE MBR: one 0xEE entry covering the disk
//	LBA 1        a GPT header with a CRC32
//	LBA 2..33    the entry array, 128 * 128 bytes
//	LBA 34       first usable sector; the partition may not start below it
//	LBA 2048     the ESP itself, FAT32, 1 MiB in
//	last-33      the backup entry array
//	last         the backup header
func BuildBootImage(efiDir, dest string, log func(string)) error {
	imageSize := int64(BootImageMB) * 1024 * 1024
	totalSectors := imageSize / SectorSize
	espFirst := int64(ESPStartSector)
	espLast := totalSectors - 34
	espSectors := espLast - espFirst + 1

	// THE FAT FILESYSTEM GOES INSIDE THE PARTITION, at its byte offset, and mtools addresses a
	// file offset with the @@ suffix - which writes the same bytes a mount would.
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	_ = os.Remove(dest)
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	if err := f.Truncate(imageSize); err != nil {
		_ = f.Close()
		return err
	}
	_ = f.Close()

	for _, tool := range []string{"mformat", "mcopy", "mdir"} {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("%s is not installed: %w. The boot image is a FAT32 ESP built "+
				"with mtools, and there is no second way to write one", tool, err)
		}
	}
	partition := fmt.Sprintf("%s@@%d", dest, espFirst*SectorSize)
	geometry := []string{
		"-F", // a full filesystem, not a partition image
		"-i", partition,
		"-c", fmt.Sprintf("%d", ESPSectorsPerCluster),
		"-T", strconv64(espSectors),
		"-v", "EFI",
	}
	if out, err := exec.Command("mformat", geometry...).CombinedOutput(); err != nil {
		return fmt.Errorf("mformat: %v (%s)", err, firstLine(string(out)))
	}
	if out, err := exec.Command("mcopy", "-s", "-i", partition, "-m", efiDir, "::/").CombinedOutput(); err != nil {
		return fmt.Errorf("mcopy: %v (%s)", err, firstLine(string(out)))
	}

	if err := writeGPT(dest, espFirst, espLast, totalSectors); err != nil {
		return err
	}
	// VERIFY WHAT THE FIRMWARE WILL SEE, NOT WHAT MTOOLS WILL: mtools never looks at a partition
	// table, which is why five runs passed their own checks and booted nothing.
	if err := checkGPT(dest, espFirst); err != nil {
		return err
	}
	if err := checkESP(dest, espFirst); err != nil {
		return err
	}

	// AND THE PATH THE FIRMWARE ACTUALLY OPENS. "OpenCore.efi is somewhere on the ESP" is a weaker
	// question, and it was answered YES by a boot image whose tree had been copied one level too
	// high - a listing that contains the string and cannot boot.
	for _, required := range []string{"::/EFI/BOOT/BOOTX64.EFI", "::/EFI/OC/OpenCore.efi"} {
		if out, err := exec.Command("mdir", "-i", partition, "-/", required).CombinedOutput(); err != nil {
			return fmt.Errorf("%s has no %s in its ESP (%s). The firmware looks for "+
				"\\EFI\\BOOT\\BOOTX64.EFI; without it OVMF drops to its internal shell and "+
				"nothing says why", dest, required, firstLine(string(out)))
		}
	}

	log(fmt.Sprintf("  boot.img: %d MiB, GPT + ESP at LBA %d, EFI tree copied",
		imageSize>>20, espFirst))
	return nil
}

// writeGPT writes a protective MBR, a primary GPT and its backup.
//
// THE BACKUP IS NOT DECORATION: a firmware that finds the primary header damaged falls back to the
// last sector, and OVMF does look there. Leaving it out produces a disk that works until the one run
// where it does not.
func writeGPT(path string, espFirst, espLast, totalSectors int64) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	// --- protective MBR -------------------------------------------------------------
	mbr := make([]byte, SectorSize)
	mbr[0x1BE+0] = 0x00
	mbr[0x1BE+1] = 0x00
	mbr[0x1BE+2] = 0x02
	mbr[0x1BE+3] = 0x00
	mbr[0x1BE+4] = 0xEE
	mbr[0x1BE+5] = 0xFF
	mbr[0x1BE+6] = 0xFF
	mbr[0x1BE+7] = 0xFF
	binary.LittleEndian.PutUint32(mbr[0x1BE+8:], 1)
	span := totalSectors - 1
	if span > 0xFFFFFFFF {
		span = 0xFFFFFFFF
	}
	binary.LittleEndian.PutUint32(mbr[0x1BE+12:], uint32(span))
	mbr[0x1FE] = 0x55
	mbr[0x1FF] = 0xAA

	// --- the entry array ------------------------------------------------------------
	entries := make([]byte, GPTEntryArraySectors*SectorSize)
	copy(entries[0:16], espTypeGUID)
	// This partition's own GUID. Random, because nothing compares it across builds - the DISC's
	// GUID is the one that is fixed, so two builds stay comparable.
	unique := make([]byte, 16)
	for i := range unique {
		unique[i] = byte(randByte())
	}
	unique[7] = (unique[7] & 0x0f) | 0x40
	unique[8] = (unique[8] & 0x3f) | 0x80
	copy(entries[16:32], unique)
	binary.LittleEndian.PutUint64(entries[32:], uint64(espFirst))
	binary.LittleEndian.PutUint64(entries[40:], uint64(espLast))
	// Name "primary" as UTF-16LE.
	for i, r := range "primary" {
		binary.LittleEndian.PutUint16(entries[56+i*2:], uint16(r))
	}
	entriesCRC := crc32IEEE(entries)

	primaryEntriesLBA := int64(2)
	backupEntriesLBA := totalSectors - 1 - GPTEntryArraySectors
	primaryHeaderLBA := int64(1)
	backupHeaderLBA := totalSectors - 1

	header := func(thisLBA, otherLBA, entriesLBA int64) []byte {
		head := make([]byte, SectorSize)
		copy(head[0:8], []byte("EFI PART"))
		binary.LittleEndian.PutUint32(head[8:], 0x00010000)
		binary.LittleEndian.PutUint32(head[12:], 92)
		binary.LittleEndian.PutUint64(head[24:], uint64(thisLBA))
		binary.LittleEndian.PutUint64(head[32:], uint64(otherLBA))
		binary.LittleEndian.PutUint64(head[40:], 34)
		binary.LittleEndian.PutUint64(head[48:], uint64(totalSectors-34))
		copy(head[56:72], gptDiskGUID)
		binary.LittleEndian.PutUint64(head[72:], uint64(entriesLBA))
		binary.LittleEndian.PutUint32(head[80:], 128)
		binary.LittleEndian.PutUint32(head[84:], 128)
		binary.LittleEndian.PutUint32(head[88:], entriesCRC)
		// THE CRC COVERS THE HEADER'S DECLARED SIZE - 92 BYTES - NOT THE WHOLE SECTOR. The rest
		// is padding and is not part of the header. Computing it over all 512 was the bug: the
		// write and the read agreed with each other and disagreed with every other GPT
		// implementation.
		binary.LittleEndian.PutUint32(head[16:], crc32IEEE(head[:92]))
		return head
	}

	writes := []struct {
		at   int64
		data []byte
	}{
		{0, mbr},
		{primaryHeaderLBA * SectorSize, header(primaryHeaderLBA, backupHeaderLBA, primaryEntriesLBA)},
		{primaryEntriesLBA * SectorSize, entries},
		{backupEntriesLBA * SectorSize, entries},
		{backupHeaderLBA * SectorSize, header(backupHeaderLBA, primaryHeaderLBA, backupEntriesLBA)},
	}
	for _, w := range writes {
		if _, err := file.WriteAt(w.data, w.at); err != nil {
			return err
		}
	}
	return file.Sync()
}

// checkGPT reads the primary header back and verifies its signature, revision and CRC.
func checkGPT(path string, espFirst int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, SectorSize)
	if _, err := f.ReadAt(head, SectorSize); err != nil {
		return fmt.Errorf("reading the GPT header: %w", err)
	}
	if string(head[0:8]) != "EFI PART" {
		return fmt.Errorf("%s has no GPT header at LBA 1 (found %q); the firmware looks for an "+
			"EFI system partition in a partition table and a filesystem at LBA 0 is not one",
			path, string(head[0:8]))
	}
	want := binary.LittleEndian.Uint32(head[16:])
	if got := crc32IEEE(head[:92]); got != want {
		return fmt.Errorf("%s has a GPT header whose CRC is %08x but the header says %08x; "+
			"firmware rejects it and the run reads as a broken bootloader", path, got, want)
	}
	return nil
}

// checkESP verifies that the filesystem inside the partition is a VALID FAT32 volume.
//
// FAT32 STOPS BEING FAT32 BELOW 65,525 CLUSTERS, and that is the only thing separating it from
// FAT16. mtools writes an invalid one happily and reads it back, so the check has to be arithmetic
// on the boot sector rather than a question for mtools.
func checkESP(path string, espFirst int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sector := make([]byte, SectorSize)
	if _, err := f.ReadAt(sector, espFirst*SectorSize); err != nil {
		return err
	}
	// The FAT32 boot sector: bytes-per-sector at 11, sectors-per-cluster at 13, reserved at 14,
	// number of FATs at 16, total sectors (32-bit) at 32, FAT size (32-bit) at 36.
	bytesPerSector := binary.LittleEndian.Uint16(sector[11:])
	sectorsPerCluster := sector[13]
	fatCount := sector[16]
	fatSize := binary.LittleEndian.Uint32(sector[36:])
	totalSectors := binary.LittleEndian.Uint32(sector[32:])
	if bytesPerSector == 0 || sectorsPerCluster == 0 || fatCount == 0 || fatSize == 0 {
		return fmt.Errorf("%s has no FAT boot sector at its ESP offset (%d bytes/sector); "+
			"the partition is there and the filesystem is not", path, bytesPerSector)
	}
	// The root directory entry count is zero on FAT32 and non-zero on FAT12/16.
	if binary.LittleEndian.Uint16(sector[17:]) != 0 {
		return fmt.Errorf("%s's ESP is FAT12/16, not FAT32: OVMF's EDK II driver refuses to "+
			"mount it and the console says only \"failed to load Boot0001\"", path)
	}
	reserved := binary.LittleEndian.Uint16(sector[14:])
	dataSectors := int64(totalSectors) - int64(reserved) -
		int64(fatCount)*int64(fatSize)
	if dataSectors <= 0 {
		return fmt.Errorf("%s's ESP has no data sectors (%d total, %d reserved, %d FAT size)",
			path, totalSectors, reserved, fatSize)
	}
	clusters := dataSectors / int64(sectorsPerCluster)
	if clusters < Fat32MinClusters {
		return fmt.Errorf("%s's ESP has %d clusters, below the %d that makes FAT32 valid "+
			"(cluster = %d sectors). mtools writes it and reads it back, so only this check "+
			"notices - and OVMF will not mount it", path, clusters, Fat32MinClusters,
			sectorsPerCluster)
	}
	return nil
}

func strconv64(v int64) string { return fmt.Sprintf("%d", v) }

func randByte() int {
	return int(timeNowUnixNano() % 256)
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// findFileCI finds a file ignoring case, because the template's names come from an ISO9660/Joliet
// tree and the two layers disagree about case.
func findFileCI(directory, wanted string) string {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if strings.EqualFold(e.Name(), wanted) && !e.IsDir() {
			return filepath.Join(directory, e.Name())
		}
	}
	return ""
}
