package run

// THE UEFI FALLBACK PATH, WRITTEN INTO A DISK THAT IS NOT RUNNING.
//
// MEASURED ON A DISK THIS REPOSITORY INSTALLED, whose install leg finished with "Congratulations!
// BlissOS-16.9.7 is installed successfully" and whose ESP held a complete loader:
//
//	/EFI/BLISSOS/BOOTX64.EFI  GRUBX64.EFI  MMX64.EFI  GRUB.CFG  ANDROID.CFG
//
// and which then booted to
//
//	BdsDxe: failed to load Boot0002 "UEFI QEMU HARDDISK QM00011" ... : Not Found
//	BdsDxe: No bootable option or device was found.
//
// THE INSTALLER'S LOADER WAS UNREACHABLE RATHER THAN MISSING. It registers /EFI/BLISSOS with
// `efibootmgr`, and that entry lands in the VARIABLES image - which this repository makes fresh for
// every boot, deliberately, so a guest cannot inherit an entry that names a disk somebody else
// wrote. Firmware with an empty NVRAM looks in exactly one place:
//
//	\EFI\BOOT\BOOTX64.EFI
//
// SO THE FIX IS A COPY TO THAT PATH, which is what easydiskbake's `bliss_esp:fallback` does. The
// difference is where the loader is read from: that installer writes /EFI/BLISSOS and its fallback
// step copies from there, which is exactly what is available here.
//
// AND THE CONFIG'S SEARCH IS REWRITTEN. It begins
//
//	search.fs_uuid 83c8dcce-dc54-4f57-982f-a1f412c5b473 root
//
// easydiskbake records that UUID as one the installer wrote BEFORE it formatted, and therefore
// stale. ON THIS DISK IT WAS CORRECT - verified by reading the ext4 superblock back - and it is
// still replaced, because a UUID is a fact about a filesystem that a reinstall changes, and a
// published artifact must not depend on one. `search --file` asks the question the UUID stood for
// ("find the partition holding the system") and answers it from the disk as it is now.

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/easydev-org/easyvm/internal/walk"
)

const (
	espSector = 512
	// C12A7328-F81F-11D2-BA4B-00A0C93EC93B, in GPT's own byte order.
	espTypeGUID = "\x28\x73\x2a\xc1\x1f\xf8\xd2\x11\xba\x4b\x00\xa0\xc9\x3e\xc9\x3b"

	fatEOC  = 0x0FFFFFFF
	fatFree = 0
	// The cluster stamp the installer's own mkdosfs chose: 512-byte sectors, 8 per cluster.
	fatEntryBytes = 4
)

// espPartition finds the EFI System Partition's byte offset and length in a disk's GPT.
func espPartition(disk string) (offset, length int64, err error) {
	header, err := readDiskAt(disk, espSector, 92)
	if err != nil {
		return 0, 0, err
	}
	if string(header[0:8]) != "EFI PART" {
		return 0, 0, fmt.Errorf("%s has no GPT header at LBA 1", filepath.Base(disk))
	}
	entryLBA := binary.LittleEndian.Uint64(header[72:80])
	count := binary.LittleEndian.Uint32(header[80:84])
	size := binary.LittleEndian.Uint32(header[84:88])
	table, err := readDiskAt(disk, int64(entryLBA)*espSector, int64(count*size))
	if err != nil {
		return 0, 0, err
	}
	for i := uint32(0); i < count; i++ {
		e := table[i*size : (i+1)*size]
		if string(e[0:16]) != espTypeGUID {
			continue
		}
		first := binary.LittleEndian.Uint64(e[32:40])
		last := binary.LittleEndian.Uint64(e[40:48])
		return int64(first) * espSector, int64(last-first+1) * espSector, nil
	}
	return 0, 0, fmt.Errorf("no EFI System Partition in %s", filepath.Base(disk))
}

// readDiskAt reads `length` bytes at `offset` from a qcow2, through qemu-io's hex dump.
//
// NOT `qemu-io read -s`: `-s` is `--snapshot` for the whole command and the read is refused with
// "Parsing error: non-numeric argument". NOT `qemu-img dd skip=` either, whose positional forms read
// the offset as a COUNT and return the disk's first sector whatever was asked for.
func readDiskAt(disk string, offset, length int64) ([]byte, error) {
	data := make([]byte, 0, length)
	const chunk = 1 << 20
	for done := int64(0); done < length; done += chunk {
		take := int64(chunk)
		if remaining := length - done; remaining < take {
			take = remaining
		}
		out, err := exec.Command("qemu-io", "-f", "qcow2", "-r", "-c",
			fmt.Sprintf("read -v %d %d", offset+done, take), disk).CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("reading %s at %d: %v (%s)", filepath.Base(disk), offset+done,
				err, firstLineOf(string(out)))
		}
		data = append(data, parseHexDump(string(out))...)
	}
	if int64(len(data)) > length {
		data = data[:length]
	}
	return data, nil
}

// parseHexDump turns `qemu-io read -v`'s lines into bytes.
//
// THE FORMAT IS `%08x:  ` THEN SIXTEEN SPACE-SEPARATED BYTE PAIRS, then two spaces and the ASCII
// column. The leading two spaces are part of it, so splitting the remainder on "  " yields the EMPTY
// string - which is how an earlier version parsed zero bytes per line and reported "no GPT header"
// about a disk that has one.
func parseHexDump(text string) []byte {
	var out []byte
	for _, line := range strings.Split(text, "\n") {
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		fields := strings.Fields(line[colon+1:])
		for i, f := range fields {
			if i >= 16 || len(f) != 2 {
				break
			}
			var b byte
			if _, err := fmt.Sscanf(f, "%02x", &b); err != nil {
				break
			}
			out = append(out, b)
		}
	}
	return out
}

// writeDiskAt writes `payload` at `offset`, through a temporary file because qemu-io reads a source.
func writeDiskAt(disk string, offset int64, payload []byte) error {
	fh, err := os.CreateTemp("", "esp-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(fh.Name())
	if _, err := fh.Write(payload); err != nil {
		fh.Close()
		return err
	}
	fh.Close()
	out, err := exec.Command("qemu-io", "-f", "qcow2", "-c",
		fmt.Sprintf("write -s %s %d %d", fh.Name(), offset, len(payload)), disk).CombinedOutput()
	if err != nil {
		return fmt.Errorf("writing %s at %d: %v (%s)", filepath.Base(disk), offset, err,
			firstLineOf(string(out)))
	}
	return nil
}

// fat32 is an ESP, read whole, edited in memory, written back by the blocks that changed.
type fat32 struct {
	img        []byte
	bps        int
	spc        int
	reserved   int
	nfats      int
	fatSectors int
	root       uint32
	cluster    int
	fatOff     int
	dataOff    int
	maxCluster uint32
}

func newFat32(image []byte) (*fat32, error) {
	// THE IMAGE IS COPIED, AND THAT IS NOT DEFENSIVE - IT IS WHAT MAKES THE WRITE-BACK WORK.
	//
	// `fat.img` and the caller's `image` were the same slice, so the write-back's
	// `if string(fat.img[off:end]) == string(image[off:end])` compared a buffer with ITSELF, decided
	// every block was unchanged, and wrote nothing at all. Measured: `esp_fallback:` reported success,
	// the disk was published, and its ESP still had no /EFI/BOOT - the step did its work in memory and
	// threw it away.
	f := &fat32{img: append([]byte(nil), image...)}
	image = f.img
	f.bps = int(binary.LittleEndian.Uint16(image[11:13]))
	f.spc = int(image[13])
	f.reserved = int(binary.LittleEndian.Uint16(image[14:16]))
	f.nfats = int(image[16])
	f.fatSectors = int(binary.LittleEndian.Uint32(image[36:40]))
	f.root = binary.LittleEndian.Uint32(image[44:48])
	f.cluster = f.bps * f.spc
	f.fatOff = f.reserved * f.bps
	f.dataOff = (f.reserved + f.nfats*f.fatSectors) * f.bps
	f.maxCluster = uint32(f.fatSectors * f.bps / fatEntryBytes)
	if f.cluster == 0 || f.maxCluster == 0 {
		return nil, fmt.Errorf("the ESP has no usable FAT: %d bytes a cluster, %d clusters",
			f.cluster, f.maxCluster)
	}
	return f, nil
}

func (f *fat32) get(c uint32) uint32 {
	off := f.fatOff + int(c)*fatEntryBytes
	return binary.LittleEndian.Uint32(f.img[off:off+4]) & 0x0FFFFFFF
}

func (f *fat32) set(c, v uint32) {
	// BOTH COPIES. A checker that reads the second table finds a filesystem that disagrees with
	// itself otherwise, and Windows is one of the checkers.
	for n := 0; n < f.nfats; n++ {
		off := (f.reserved+n*f.fatSectors)*f.bps + int(c)*fatEntryBytes
		binary.LittleEndian.PutUint32(f.img[off:off+4], v&0x0FFFFFFF)
	}
}

func (f *fat32) clusterAt(c uint32) int {
	return f.dataOff + int(c-2)*f.cluster
}

func (f *fat32) allocate(count int) ([]uint32, error) {
	var found []uint32
	for c := uint32(2); c < f.maxCluster && len(found) < count; c++ {
		if f.get(c) == fatFree {
			found = append(found, c)
		}
	}
	if len(found) < count {
		return nil, fmt.Errorf("the ESP has room for %d clusters, needs %d", len(found), count)
	}
	for i, c := range found {
		next := uint32(fatEOC)
		if i+1 < len(found) {
			next = found[i+1]
		}
		f.set(c, next)
	}
	return found, nil
}

func (f *fat32) chain(c uint32) []uint32 {
	var out []uint32
	seen := map[uint32]bool{}
	for c >= 2 && c < f.maxCluster && c < 0x0FFFFFF8 && !seen[c] {
		seen[c] = true
		out = append(out, c)
		c = f.get(c)
	}
	return out
}

func (f *fat32) readChain(c uint32) []byte {
	var out []byte
	for _, cl := range f.chain(c) {
		start := f.clusterAt(cl)
		out = append(out, f.img[start:start+f.cluster]...)
	}
	return out
}

type fatEntry struct {
	name    string
	attr    byte
	cluster uint32
	size    uint32
}

func (f *fat32) entries(c uint32) []fatEntry {
	data := f.readChain(c)
	var out []fatEntry
	for off := 0; off+32 <= len(data); off += 32 {
		e := data[off : off+32]
		if e[0] == 0 {
			break
		}
		if e[0] == 0xE5 || e[11]&0x0F == 0x0F || e[11]&0x08 != 0 {
			continue
		}
		stem := strings.TrimRight(string(e[0:8]), " ")
		ext := strings.TrimRight(string(e[8:11]), " ")
		name := stem
		if ext != "" {
			name += "." + ext
		}
		// FstClusHI AT 20 AND FstClusLO AT 26 - the order the names are written in the spec's table
		// is the OPPOSITE of their offsets, and reading them the other way turns a populated
		// directory into an empty one. Measured: `/EFI` read as cluster 196608, past the table.
		hi := uint32(binary.LittleEndian.Uint16(e[20:22]))
		lo := uint32(binary.LittleEndian.Uint16(e[26:28]))
		out = append(out, fatEntry{
			name: name, attr: e[11], cluster: hi<<16 | lo,
			size: binary.LittleEndian.Uint32(e[28:32]),
		})
	}
	return out
}

func (f *fat32) find(c uint32, name string) *fatEntry {
	for _, e := range f.entries(c) {
		if strings.EqualFold(e.name, name) {
			return &e
		}
	}
	return nil
}

func (f *fat32) dirent(name string, attr byte, cluster, size uint32) []byte {
	e := make([]byte, 32)
	if name == "." || name == ".." {
		// `".".partition(".")` gives three empty strings, so these two need their own branch -
		// without it the first two entries of every directory come out nameless.
		copy(e[0:11], []byte(fmt.Sprintf("%-11s", name)))
	} else {
		stem, ext, _ := strings.Cut(strings.ToUpper(name), ".")
		copy(e[0:8], []byte(fmt.Sprintf("%-8s", stem)))
		copy(e[8:11], []byte(fmt.Sprintf("%-3s", ext)))
	}
	e[11] = attr
	binary.LittleEndian.PutUint16(e[20:22], uint16(cluster>>16))
	binary.LittleEndian.PutUint16(e[26:28], uint16(cluster))
	binary.LittleEndian.PutUint32(e[28:32], size)
	return e
}

func (f *fat32) freeSlot(dir uint32) (int, error) {
	chain := f.chain(dir)
	for _, c := range chain {
		base := f.clusterAt(c)
		for off := 0; off < f.cluster; off += 32 {
			if f.img[base+off] == 0x00 || f.img[base+off] == 0xE5 {
				return base + off, nil
			}
		}
	}
	// FULL: one more cluster, linked to the end and zeroed.
	got, err := f.allocate(1)
	if err != nil {
		return 0, err
	}
	f.set(chain[len(chain)-1], got[0])
	f.set(got[0], fatEOC)
	start := f.clusterAt(got[0])
	copy(f.img[start:start+f.cluster], make([]byte, f.cluster))
	return start, nil
}

func (f *fat32) mkdir(parent uint32, name string) (uint32, error) {
	if e := f.find(parent, name); e != nil {
		return e.cluster, nil
	}
	got, err := f.allocate(1)
	if err != nil {
		return 0, err
	}
	cluster := got[0]
	start := f.clusterAt(cluster)
	copy(f.img[start:start+f.cluster], make([]byte, f.cluster))
	copy(f.img[start:start+32], f.dirent(".", 0x10, cluster, 0))
	parentRef := parent
	if parent == f.root {
		parentRef = 0
	}
	copy(f.img[start+32:start+64], f.dirent("..", 0x10, parentRef, 0))
	slot, err := f.freeSlot(parent)
	if err != nil {
		return 0, err
	}
	copy(f.img[slot:slot+32], f.dirent(name, 0x10, cluster, 0))
	return cluster, nil
}

func (f *fat32) writeFile(dir uint32, name string, payload []byte) error {
	need := (len(payload) + f.cluster - 1) / f.cluster
	if need == 0 {
		need = 1
	}
	clusters, err := f.allocate(need)
	if err != nil {
		return err
	}
	for i, c := range clusters {
		start := f.clusterAt(c)
		piece := make([]byte, f.cluster)
		if i*f.cluster < len(payload) {
			copy(piece, payload[i*f.cluster:min(len(payload), (i+1)*f.cluster)])
		}
		copy(f.img[start:start+f.cluster], piece)
	}
	slot, err := f.freeSlot(dir)
	if err != nil {
		return err
	}
	copy(f.img[slot:slot+32], f.dirent(name, 0x20, clusters[0], uint32(len(payload))))
	return nil
}

// espFallback copies a disk's EFI loader tree to `\EFI\BOOT` and repairs its config's search.
func espFallback(disk, from string) error {
	offset, length, err := espPartition(disk)
	if err != nil {
		return err
	}
	image, err := readDiskAt(disk, offset, length)
	if err != nil {
		return err
	}
	if len(image) < 512 || image[510] != 0x55 || image[511] != 0xAA {
		return fmt.Errorf("the ESP in %s has no FAT boot sector", filepath.Base(disk))
	}
	fat, err := newFat32(image)
	if err != nil {
		return err
	}

	efiEntry := fat.find(fat.root, "EFI")
	if efiEntry == nil {
		return fmt.Errorf("the ESP in %s has no /EFI: this is not a disk an installer finished on",
			filepath.Base(disk))
	}
	// `from` is a slash path inside the ESP, defaulting to the directory BlissOS's own installer
	// writes. Its last element is the directory name to look for.
	// THE LOADER'S DIRECTORY IS THE FILE'S TO NAME, AND THE DEFAULT USED TO BE `BLISSOS`.
	//
	// A hardcoded distribution name in a general primitive is a bug waiting for the second caller:
	// the first walking style that was not Android would have copied /EFI/BLISSOS on a disk that has
	// /EFI/ubuntu, and the error would say "there is no loader to copy" about a loader that is there.
	// `esp_from` is required now - see doESPFallback - so this cannot be reached empty.
	leaf := strings.ToUpper(strings.Trim(strings.TrimPrefix(from, "/"), "/"))
	if i := strings.LastIndexByte(leaf, '/'); i >= 0 {
		leaf = leaf[i+1:]
	}
	src := fat.find(efiEntry.cluster, leaf)
	if src == nil {
		return fmt.Errorf("the ESP has /EFI but no /EFI/%s: there is no loader to copy", leaf)
	}

	boot, err := fat.mkdir(efiEntry.cluster, "BOOT")
	if err != nil {
		return err
	}
	copied := 0
	for _, e := range fat.entries(src.cluster) {
		name := strings.ToUpper(e.name)
		if e.attr&0x10 != 0 || !strings.HasSuffix(name, ".EFI") {
			continue
		}
		payload := fat.readChain(e.cluster)
		if int(e.size) < len(payload) {
			payload = payload[:e.size]
		}
		if err := fat.writeFile(boot, name, payload); err != nil {
			return err
		}
		copied++
	}

	// THE CONFIG'S SEARCH IS REPLACED, NOT KEPT. See this file's header for the measurement.
	if cfg := fat.find(src.cluster, "GRUB.CFG"); cfg != nil {
		text := string(fat.readChain(cfg.cluster)[:cfg.size])
		var out []string
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "search.fs_uuid") {
				out = append(out, "search --no-floppy --set=root --file /android-2024-10-11/kernel")
				continue
			}
			out = append(out, line)
		}
		rewritten := strings.Join(out, "\n")
		if err := fat.writeFile(boot, "GRUB.CFG", []byte(rewritten)); err != nil {
			return err
		}
	}
	// THE MENU THE CONFIG SOURCES LIVES BESIDE IT.
	if menu := fat.find(src.cluster, "ANDROID.CFG"); menu != nil {
		payload := fat.readChain(menu.cluster)
		if int(menu.size) < len(payload) {
			payload = payload[:menu.size]
		}
		if err := fat.writeFile(boot, "ANDROID.CFG", payload); err != nil {
			return err
		}
	}

	// THE WRITE-BACK IS THE BLOCKS THAT CHANGED. A whole 260 MiB write allocates 260 MiB of clusters
	// in the qcow2 even though ~5 MiB changed, which grows the published image for nothing.
	const block = 64 << 10
	for off := 0; off < len(fat.img); off += block {
		end := off + block
		if end > len(fat.img) {
			end = len(fat.img)
		}
		if string(fat.img[off:end]) == string(image[off:end]) {
			continue
		}
		if err := writeDiskAt(disk, offset+int64(off), fat.img[off:end]); err != nil {
			return err
		}
	}
	if copied == 0 {
		return fmt.Errorf("/EFI/%s held no .EFI file: the disk still has no loader", leaf)
	}
	return nil
}

// doESPFallback is the step: `esp_fallback:` names the disk, `esp_from:` the loader directory.
func (e *Executor) doESPFallback(s walk.Step) error {
	// TWO FORMS, ONE ACTION. The mapping says what each part is; the flat form is what the files use.
	disk := s.Get("esp_fallback")
	from := s.Get("esp_from")
	if m, ok := s.Spec["esp_fallback"].(map[string]any); ok {
		if v, ok := m["disk"].(string); ok {
			disk = v
		}
		if v, ok := m["from"].(string); ok {
			from = v
		}
		for k := range m {
			if k != "disk" && k != "from" {
				return fail(s.Name, "`esp_fallback:` has no key %q; the mapping form is "+
					"{disk, from}", k)
			}
		}
	}
	if disk == "" || disk == "disk" {
		disk = e.bootDisk()
		if disk == "" {
			return fail(s.Name, "`esp_fallback:` names no disk and this stage has not built one: "+
				"write `esp_fallback: disk` after the `disk:` step, or name a file")
		}
	}
	if !filepath.IsAbs(disk) {
		inWork := filepath.Join(e.Work, disk)
		beside := filepath.Join(filepath.Dir(e.File.Path), disk)
		switch {
		case fileExists(inWork):
			disk = inWork
		case fileExists(beside):
			disk = beside
		}
	}
	e.Driver.Log(fmt.Sprintf("  esp fallback: %s", filepath.Base(disk)))
	if from == "" {
		return fail(s.Name, "`esp_fallback:` needs the loader's directory inside the ESP: "+
			"`esp_fallback: {disk: disk, from: /EFI/BLISSOS}`. It has no default - a "+
			"distribution's own name baked into a primitive is wrong for the next guest")
	}
	if err := espFallback(disk, from); err != nil {
		return fail(s.Name, "%v", err)
	}
	return nil
}
