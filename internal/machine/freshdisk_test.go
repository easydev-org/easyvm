package machine

import "testing"

// AN INSTALLED macOS DISK BOOTED WITHOUT `recovery:` MUST KEEP ITS WRITES.
//
// THIS IS easydev-org/easyvm#3's DEFECT 2, AND IT COST THREE FULL INSTALLS TO FIND. The rule was
//
//	installing() = b.Recovery != ""          // "is an installer attached"
//	WantsOverlay() = !installing()           // so: no recovery means THROWAWAY DIFF
//
// and `ReleaseDiff` deletes that diff on destroy. A walk that booted an already-installed macOS disk
// with a plain `boot: true` therefore wrote every Setup Assistant pane into a file that was thrown
// away, and the visible symptom was a wizard that restarted at the country list every run - with no
// error anywhere and the base disk byte-for-byte unchanged (measured: 38.40 GiB before and after).
//
// THE QUESTION IS NOT "IS AN INSTALLER ATTACHED" BUT "DID THIS RUN MAKE THE DISK", so the two are now
// separate facts and this test pins the pair:
//
//	fresh disk            runs on itself (no diff to lose), installers or not
//	a disk from elsewhere writes into a diff that `compress:` flattens
func TestADiskThatCameFromSomewhereKeepsItsWrites(t *testing.T) {
	cases := []struct {
		name      string
		boot      Boot
		wantDiff  bool
		whyItMatters string
	}{
		{
			name:      "a fresh macOS install runs on its own disk",
			boot:      Boot{OS: MacOS, Disk: "d.qcow2", Recovery: "r.dmg", FreshDisk: true},
			wantDiff:  false,
			whyItMatters: "a `disk_gb:` step just made this disk; there is nothing behind it to protect",
		},
		{
			name:      "an INSTALLED macOS disk with no recovery attached still keeps its writes",
			boot:      Boot{OS: MacOS, Disk: "d.qcow2", FreshDisk: false},
			wantDiff:  true,
			whyItMatters: "#3's defect 2: without a diff, a boot that writes to the disk is fine; WITH the old " +
				"rule this got a throwaway diff because no recovery was attached, and every write was deleted",
		},
		{
			name:      "a Linux install on a fresh disk",
			boot:      Boot{OS: Linux, Disk: "d.qcow2", ISO: "i.iso", FreshDisk: true},
			wantDiff:  false,
			whyItMatters: "same rule for every family - the disk is either this run's or it is not",
		},
		{
			name:      "a pulled Linux disk",
			boot:      Boot{OS: Linux, Disk: "d.qcow2", FreshDisk: false},
			wantDiff:  true,
			whyItMatters: "a published artifact must survive a stage that only reads it",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.boot.WantsOverlay()
			if got != tc.wantDiff {
				t.Fatalf("WantsOverlay() = %v, want %v\n\t%s", got, tc.wantDiff, tc.whyItMatters)
			}
		})
	}
}

// Discardable IS NOT THE NEGATION OF WantsOverlay, AND THAT IS THE WHOLE FIX FOR DEFECT 3.
//
// The two used to be the same expression, so these two rows of a truth table were impossible:
//
//	writes persist (no overlay)   AND   TRIM reaches the disk
//
// and #3 measured the cost on an installed macOS 26 disk: the guest reported 12 GiB used of 120, the
// host file was 38.4 GiB, and 26 GiB of it was blocks the guest had already freed. `qemu-img convert
// -c` cannot remove them because to it they look live. Running the same disk with `discard=unmap` and
// emptying the guest's caches gave 19.7 GiB, WRITABLE, at 6% fragmentation.
//
// THE ONE CASE THAT MUST STAY FALSE IS AN INSTALLER: the note above `installing()` records 17 GiB of
// the installer's own staging data being deallocated under its APFS snapshot when discards were
// honoured. So the rule is "not while an installer is writing", and a disk that simply came from
// somewhere may trim.
func TestATrimMayReachAnInstalledDiskButNeverAnInstaller(t *testing.T) {
	cases := []struct {
		name        string
		boot        Boot
		wantDiscard bool
	}{
		{
			name:        "an installer must never see a trim",
			boot:        Boot{OS: MacOS, Disk: "d.qcow2", Recovery: "r.dmg", Shape: macOSShape},
			wantDiscard: false,
		},
		{
			name:        "a fresh Linux install must never see a trim",
			boot:        Boot{OS: Linux, Disk: "d.qcow2", ISO: "i.iso"},
			wantDiscard: false,
		},
		{
			name:        "an INSTALLED macOS disk MAY trim, and this is the row that used to be impossible",
			boot:        Boot{OS: MacOS, Disk: "d.qcow2", Shape: macOSShape},
			wantDiscard: true,
		},
		{
			name:        "a pulled Linux disk MAY trim",
			boot:        Boot{OS: Linux, Disk: "d.qcow2"},
			wantDiscard: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.boot.Discardable(); got != tc.wantDiscard {
				t.Fatalf("Discardable() = %v, want %v", got, tc.wantDiscard)
			}
		})
	}

	// AND THE PROPERTY THAT WAS BROKEN, STATED AS A PROPERTY: there must exist a configuration in
	// which A DISK'S WRITES REACH THE FILE THAT IS PUBLISHED *AND* ITS TRIMS DO TOO.
	//
	// `WantsOverlay()` IS NOT THE OPPOSITE OF "KEEPS ITS WRITES" - a diff is flattened by `compress:`
	// at the end of a stage, which is how every pulled disk is written to. What the old code could not
	// express was the other combination: a disk with NO diff (writes go straight to the file) that is
	// ALSO allowed to trim. That is a `FreshDisk` boot which is no longer installing - the moment the
	// installer has finished and the disk is being cleaned up, which is exactly when 26 GiB of freed
	// blocks need to leave the file.
	keepsWrites := Boot{OS: MacOS, Disk: "d.qcow2", FreshDisk: true, Shape: macOSShape}
	if keepsWrites.WantsOverlay() {
		t.Fatalf("a fresh disk opened a throwaway diff; there is nothing behind it to protect, and " +
			"the diff is one more file to flatten")
	}
	if !keepsWrites.Discardable() {
		t.Fatalf("a fresh, non-installing disk cannot be trimmed. The only reason this row was ever " +
			"impossible is that Discardable() was `!installing()` and WantsOverlay() was the same " +
			"expression, so the published qcow2 carries every block the guest ever freed - #3 " +
			"measured 26 GiB of them")
	}

	// THE OLD EXPRESSION WOULD FAIL THIS: with both derived from `installing()`, any boot that could
	// trim was a boot whose writes went into a diff, and any boot without a diff refused to trim.
	// Stated as the identity the fix removes, so a change that reintroduces it fails here.
	installing := Boot{OS: MacOS, Disk: "d.qcow2", Recovery: "r.dmg", Shape: macOSShape}
	if installing.WantsOverlay() == installing.Discardable() {
		t.Fatalf("WantsOverlay and Discardable agree on an installing boot, which is the shape that " +
			"made the two impossible to combine")
	}
}

// macOSShape is the macOS machine, written here rather than loaded from the registry.
//
// THE SHAPE IS OWNED BY `easyvm-macos` NOW - that repository's `shapes/macos.yaml` is the definition,
// and it reaches a daemon through `stage.yml`'s `shapes_dir:`. This build embeds only `linux` and
// `microvm`, the two machines a bare image can start with nothing mounted beside it, so a test about
// macOS behaviour has to state the machine it is talking about.
//
// ONE FIELD, BECAUSE ONE FIELD IS WHAT `installing()` ASKS: macOS's installer is the recovery image on
// a USB device, so a boot that attaches one is the boot that writes, and a boot that does not is an
// installed system whose frees may reach the file.
var macOSShape = Shape{Name: "macos", InstallMedium: MediumRecovery}
