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
		"boot", "shutdown", "reboot",
		"disk",
		"record", "compress",
		"require_ssh", "ssh", "scp", "script", "write", "fetch",
		"desktop",
		"share_serve", "share_mount",
		"type", "key", "double", "at",
		"wait_for", "screenshot",
		"unless", "until", "until_any", "until_console", "expect", "expect_any", "at_window",
		"at_button", "host_log", "run", "disk_settle",
		"repeat_until",
		"demo_script",
		"wait",
	}
}
