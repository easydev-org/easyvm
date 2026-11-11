package walk

// THE PYTHON EXTENSIONS, AND WHAT EACH BECOMES.
//
// A `python: module:function` step called into a stage's own code. That is the one thing in these
// files that is not data, and it is the reason 26 of the 54 walkthroughs do not load yet. Each row
// below is a real step in a real file; the count is how many files name it.
//
// THE POINT OF THIS LIST IS THAT THE WORK IS FINITE AND NAMED. The extensions are not 32 unrelated
// pieces of logic - they are seven groups, and three of them (bootstrap_files, panes, footer) cover
// 27 of the 32 call sites.
//
//	15  bootstrap_files:launcher          write the share's launch script for this family
//	 8  footer:press_footer               press a footer button, with its settle
//	 6  panes:advance | panes:install_icon | panes:finish
//	 5  ssh_panes:*                       wait for a desktop, dismiss post-login windows, login
//	 4  bootstrap_files:{macos_plist, macos_mount_script, macos_mount_plist, windows_task}
//	 4  macos_dev_asset:*                 fetch Xcode, fetch packages, copy them in
//	 2  vm_configure_asset:fetch_guest_agent
//	 2  bliss_esp:*, 2 bliss_ssh:*, 1 setup_panes:select_target_disk, 1 primitives_demo,
//	    1 sequoia_panes:apple_account, 1 ghostbsd_steps:wait_for_live_desktop
//
// WHAT EACH BECOMES IS A STEP TYPE, NOT A FUNCTION CALL. The replacement is not "call this Go
// function" - it is a key in the vocabulary that says what the step DOES, so the file keeps
// describing work rather than naming code:
//
//	python: bootstrap_files:launcher      ->  write_launcher: { family: windows }
//	python: footer:press_footer           ->  press_footer: { settle: 3 }
//	python: vm_configure_asset:fetch_guest_agent -> fetch: { asset: guest_agent }
//	python: macos_dev_asset:fetch_xcode   ->  fetch: { asset: xcode, url: ... }
//
// THAT MIGRATION IS THE REMAINING BULK OF THE PORT, AND IT IS DELIBERATELY NOT DONE YET: a key
// added to the vocabulary without its implementation would make the file load and the step do
// nothing, which is the exact failure this package exists to prevent.
//
// STATUS, STATED PLAINLY:
//
//	DONE           the Windows and macOS files, which is what this repository ships - 10 of 10
//	               load, and none of them names a `python:` step any more
//	LEFT           the Linux, BSD and Android files still in the other repository, and the
//	               `not implemented` branches named at the end of this comment
//
// THE TABLE BELOW IS KEPT AS A RECORD OF WHAT EACH EXTENSION WAS, because that is the question a
// reader has when comparing the two implementations - not because any of them is still pending.
//
// THREE KEYS STILL REACH A `not implemented` BRANCH, and that is deliberate rather than unfinished
// in the same sense: `copy_in` needs a fetched asset, and the two asset kinds it names (Xcode,
// packages) are large transfers that have not been written. A step that reaches one says
// "not implemented", which is distinct from a failure - the reader needs to know whether the guest
// did not do it or this build cannot ask.

// Extension is one `python:` reference found in the real files, and what it should become.
type Extension struct {
	// Ref is the `module:function` the file names today.
	Ref string
	// Files is how many walkthroughs use it, so the work can be ordered by what unblocks most.
	Files int
	// Becomes is the step key that will replace it, and the shape of its value.
	Becomes string
	// Why is what the extension actually does, from reading it.
	Why string
}

// Extensions is the full migration list, most-used first.
var Extensions = []Extension{
	{Ref: "bootstrap_files:launcher", Files: 15,
		Becomes: "write_launcher: {family: windows|macos|linux}",
		Why: "writes the share's launch script and the plist/task that runs it at login. The " +
			"body of the launcher belongs in Go because it is HOST code: it decides what the " +
			"guest runs, and putting it through a shell would be a second program to read."},
	{Ref: "footer:press_footer", Files: 8,
		Becomes: "press_footer: {settle: 3}",
		Why: "presses a footer button (Continue, Next) that sits at the bottom of a wizard pane, " +
			"sleeping between the press and the next look because the pane animates."},
	{Ref: "panes:advance", Files: 6,
		Becomes: "advance: true",
		Why: "the default way through an unknown pane: press Return, after moving focus to the " +
			"pane's own button rather than relying on what was focused."},
	{Ref: "ssh_panes:wait_for_desktop", Files: 2,
		Becomes: "desktop: {family: macos}",
		Why: "the wallpaper test - the only thing that can confirm a macOS password was accepted, " +
			"because the desktop has no readable text. `desktop:` already exists in the vocabulary."},
	{Ref: "ssh_panes:dismiss_post_login_windows", Files: 2,
		Becomes: "dismiss: {windows: post_login}",
		Why: "clicks away the Keyboard Setup Assistant and the other first-login overlays, which " +
			"swallow the next keystroke. One place, because a missed overlay is the same bug " +
			"every time."},
	{Ref: "vm_configure_asset:fetch_guest_agent", Files: 2,
		Becomes: "fetch: {asset: guest_agent, version: ...}",
		Why: "downloads virtio-win and the qemu guest agent MSI on the host, with a checksum. " +
			"Host code: it is a download, not a screen."},
	{Ref: "ssh_panes:wait_for_login_window", Files: 1,
		Becomes: "wait_for: {control: login_window}",
		Why: "waits for the login window, which has no readable title - only the password " +
			"field's placeholder, twelve pixels tall, so this reads a crop at 6x."},
	{Ref: "setup_panes:select_target_disk", Files: 1,
		Becomes: "repeat_until: {number_between: [60, 200], actions: [{key: up}]}",
		Why: "IT BECAME A LOOP, NOT A PRIMITIVE, AND THAT WAS A CORRECTION. What it does is press " +
			"Up, read the detail pane's capacity and stop when the number is in a range - a loop " +
			"and a decision, both of which were hidden in Go. `repeat_until:` is that loop, and the " +
			"range is now a line in the file where a reader can see it."},
	{Ref: "sequoia_panes:apple_account", Files: 1,
		Becomes: "a pane with a `do:` list of four actions, then an expect_any: verdict",
		Why: "IT WAS NEVER A PRIMITIVE. The function's own comment gives the four actions and " +
			"their timings, and says why the last two are unconditional: the sheet only exists if " +
			"the click worked, and the same coordinate is empty space once the pane has gone. That " +
			"is a `do:` list, and hiding it hid the timings."},
	{Ref: "macos_dev_asset:*", Files: 4,
		Becomes: "fetch: {asset: xcode|packages}, copy: {asset: ...}",
		Why: "Xcode is 2.7 GiB from archive.org with a SHA-1 check, copied in and expanded. Host " +
			"code, and the largest single download in the repository."},
	{Ref: "primitives_demo:write_script", Files: 1,
		Becomes: "write: {path: ..., body: ...}",
		Why: "a demonstration step for the primitives walkthrough, which exists to exercise the " +
			"vocabulary itself."},
	{Ref: "bliss_ssh:build_payload", Files: 1,
		Becomes: "build_payload: {kind: ssh}",
		Why: "Android has no sshd until a stage installs one, so this builds the archive that " +
			"gets it there over adb."},
	{Ref: "bliss_ssh:push_payload", Files: 1,
		Becomes: "push_payload: {over: adb}",
		Why:     "the other half: copies that archive in."},
	{Ref: "bliss_esp:verify", Files: 1,
		Becomes: "esp: {action: verify}",
		Why: "reads the EFI partition back as the verdict on an Android install - the disk's " +
			"only readable proof that the installer finished."},
	{Ref: "bliss_esp:fallback", Files: 1,
		Becomes: "esp: {action: fallback_loader}",
		Why: "puts Grub2 at \\EFI\\BOOT\\BOOTX64.EFI so the firmware finds a loader without a " +
			"NVRAM entry."},
	{Ref: "ghostbsd_steps:wait_for_live_desktop", Files: 1,
		Becomes: "desktop: {family: freebsd}",
		Why:     "the same wallpaper-style wait, for a live medium that has never been installed."},
}
