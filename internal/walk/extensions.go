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
//	loaded        28 of 54 walkthroughs   (the 26 that fail are all and only `python:` steps)
//	vocabulary    85 keys, complete       (no unknown-key failure in any of the 54)
//	step engines  ~10 of 85 implemented   (load, validate, substitute, test; the rest to come)

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
		Becomes: "select_disk: {by: capacity}",
		Why: "picks the target disk in Disk Utility by its size rather than by row number, " +
			"because the row order depends on what the installer enumerated."},
	{Ref: "sequoia_panes:apple_account", Files: 1,
		Becomes: "apple_account: {action: skip}",
		Why: "the Apple Account pane differs between macOS 15 and 26 - a menu on one, a text " +
			"link on the other - and this is where that release check lives."},
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
