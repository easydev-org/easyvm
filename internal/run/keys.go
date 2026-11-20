package run

// ExecutableKeys reports the step keys this build can actually act on.
//
// IT IS WRITTEN NEXT TO THE DISPATCHER ON PURPOSE. A hand-kept list would be a second answer to
// "what can this build do", and the two would drift the moment a branch was added - which is the
// same failure the step vocabulary exists to prevent, one level up. The one test in this package
// checks that every key named here has a branch in `runOne`.
func ExecutableKeys() []string {
	return []string{
		// acted on by runOne's switch
		"boot", "boot_from", "shutdown", "acpi", "qga", "wait_until",
		"disk",
		"compress",
		"needs", "require", "require_ssh", "ssh", "scp", "write", "fetch", "asset", "build", "generate",
		"desktop",
		"type", "key", "double", "click",
		"screenshot",
		// THE THREE-STEP MODEL. `ocr:` reads one region and names it; `check:` answers from that
		// name; `loop:` is the three parts written down. ONLY THESE THREE ARE DISPATCHED: `says`,
		// `says_any` and `max` are sub-keys of `check:` and `loop:` - a sub-key is not a step, and
		// claiming it here is what the dispatch test exists to refuse.
		"check", "loop", "if",
		// `see:` MEASURES and `want:` DECIDES - the pair that makes a screen test data rather than a
		// function in the binary. Both have a branch in runOne.
		"see",
		// `settle:` IS BOTH A STEP AND A MODIFIER - it absorbs `wait:` and `wait_after:`, which the
		// files use 745 times between them.
		"settle",
		"when", "when_not", "when_not_any", "do",
		// `expect:`/`expect_any:` ARE NOT HERE ON PURPOSE: they have no branch of their own. They
		// are an ARGUMENT of `run:` and `doRun` calls `doExpectAny` after the typing, because a
		// branch at the top level would take a step that both types and checks and run only the
		// check.
		"until", "until_any", "at_color",
		"at_button", "at_bottom", "run", "disk_settle", "do",
		// A DISK EDIT, NOT A GUEST ACTION. It has its own branch because the guest it belongs to has
		// usually just been shut down - qemu holds the qcow2's write lock while it runs.
		"esp_fallback",
		// ANDROID'S TRANSPORT: adbd on 5555, no sshd and no password.
		"adb", "adb_connect", "adb_privileged",
		"wait",
	}
}
