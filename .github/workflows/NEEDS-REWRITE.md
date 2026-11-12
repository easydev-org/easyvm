# Walkthroughs that must be rewritten, not adjusted

## mx 25.3 — the installer is a TEXT MENU, not Calamares

MEASURED, from `mx-install`'s own recording at 200s:

	Welcome to MX-25.3_Xfce (Infinity)!

	===> Language - Keyboard - Timezone
	     Language (lang=en_US)          <- highlighted
	     Keyboard (kbd=us)
	     Timezone (tz=America/New_York)
	     Reset
	<=== Back to main menu

	Use ↑ and ↓, Hit »ENTER« to select/deselect.
	Press »e« to edit, »ESC« to go back.

EVERY STEP IN `linux/mx/steps/install.yaml` ASSUMES A CALAMARES GUI - `at: [1482, 889]` and
`region: [690, 225, 1560, 265]` - and the machine boots a full-screen character-cell menu instead. The
walk cannot be adjusted: it needs arrow keys, Enter and Escape, and its own panes read by
`until:` on console text rather than by clicking rectangles.

The live session ALSO renders at 1024x768 rather than 1920x1080 (the log says so: "the framebuffer is
1024x768 and this region was measured at 1920x1080"), so every coordinate in the file is doubly wrong.

## opensuse Leap 16.1 — there is no live desktop at all

`get.opensuse.org/leap/16.1` publishes:

	Leap-16.1-offline-installer-x86_64.install.iso   Agama, the new installer
	Leap-16.1-OEM.x86_64-KDE.iso                     an OEM image; first boot is installed
	Leap-16.1-Immutable.x86_64-Base-SelfInstall.iso  self-install

and NO KDE-Live or GNOME-Live image. The `opensuse-tumbleweed` walkthrough drives YaST2 from a live KDE
desktop, which is a product Leap 16.1 does not ship.
