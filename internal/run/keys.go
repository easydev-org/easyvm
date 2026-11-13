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
		"boot", "boot_from", "shutdown", "reboot",
		"disk",
		"record", "compress", "snapshot",
		"require_ssh", "ssh", "scp", "script", "write", "asset", "build", "generate",
		"desktop",
		"share_serve", "share_mount",
		"type", "key", "double", "click",
		"wait_for", "screenshot",
		// THE THREE-STEP MODEL. `ocr:` reads one region and names it; `check:` answers from that
		// name; `loop:` is the three parts written down. ONLY THESE THREE ARE DISPATCHED: `says`,
		// `says_any` and `max` are sub-keys of `check:` and `loop:` - a sub-key is not a step, and
		// claiming it here is what the dispatch test exists to refuse.
		"ocr", "check", "loop", "if",
		"unless", "unless_any", "until", "until_any", "until_console", "expect", "expect_any", "at_color",
		"at_button", "host_log", "run", "disk_settle", "do",
		"repeat_until",
		"demo_script",
		"wait",
	}
}
