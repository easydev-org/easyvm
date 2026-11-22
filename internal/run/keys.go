package run

// ExecutableKeys reports the step keys this build can actually act on.
//
// IT IS WRITTEN NEXT TO THE DISPATCHER ON PURPOSE. A hand-kept list would be a second answer to
// "what can this build do", and the two would drift the moment a branch was added. The test in this
// package checks that every key named here has a branch in `runOne`, and that every branch is named.
//
// THE FIRST GROUP IS THE 2.0 VOCABULARY. The second is the shapes those verbs compile to, which the
// executor still dispatches on because a shape is what it knows how to run - `press` carries `click`
// or `text`, `run` carries the command, `copy` carries two paths.
func ExecutableKeys() []string {
	return []string{
		// THE VERBS.
		"boot", "input", "run", "copy", "read", "check", "control", "if", "stop", "repeat",
		// THE TRANSPORTS AND THE PREPARATION A VERB MAY NAME.
		"ssh", "adb", "adb_connect", "adb_privileged",
		"fetch", "asset", "build", "generate", "write",
		// THE MACHINE.
		"disk", "disk_settle",
		"acpi", "qga", "wait_until", "esp_fallback",
		// THE SCREEN SHAPES.
		"read", "screenshot", "desktop", "at_color", "at_bottom", "at_button",
		// THE GATES AND THE PAUSES.
		"when", "when_not", "when_not_any", "do", "until", "until_any",
		"needs", "require", "wait", "settle",
	}
}
