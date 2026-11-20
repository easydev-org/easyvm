#!/bin/bash
set -euo pipefail

# THE PASSWORD IS AN ARGUMENT BECAUSE SUDO OVER SSH HAS NO TTY: `sudo` on this channel answers
# "a password is required" and has nowhere to read it from.
PASSWORD='{{ password }}'
XIP=/tmp/Xcode.xip
DEST=/Applications
WORK=/Users/{{ user }}/xcode-expand

# `xip -x` WRITES THE EXPANDED BUNDLE BESIDE A DIRECTORY, so the working directory decides
# where 15 GiB lands. It goes in the user's home, not /tmp, whose volume is small on macOS.
rm -rf "$WORK"
mkdir -p "$WORK"
cd "$WORK"
echo "expanding $XIP (this prints nothing for about twenty minutes)"
xip -x "$XIP"
echo "expanded"

APP="$(/usr/bin/find "$WORK" -maxdepth 2 -name 'Xcode*.app' -type d | head -1)"
if [ -z "$APP" ]; then
  echo "NO Xcode.app in $WORK after xip -x" >&2
  ls -la "$WORK" >&2
  exit 1
fi
echo "found $APP"

# MOVED WITH `ditto`, which preserves the code signature and the extended attributes that a
# plain `mv` across volumes can drop.
rm -rf "$DEST/Xcode.app"
/usr/bin/ditto "$APP" "$DEST/Xcode.app"

# THE LICENCE FIRST, BECAUSE EVERYTHING ELSE ASKS XCODE A QUESTION.
if [ -x /usr/bin/sudo ]; then
  printf '%s\n' "$PASSWORD" | sudo -S -p '' /usr/bin/xcode-select -s "$DEST/Xcode.app/Contents/Developer"
  printf '%s\n' "$PASSWORD" | sudo -S -p '' /usr/bin/xcodebuild -license accept
  printf '%s\n' "$PASSWORD" | sudo -S -p '' /usr/bin/xcodebuild -runFirstLaunch
else
  /usr/bin/xcode-select -s "$DEST/Xcode.app/Contents/Developer"
  /usr/bin/xcodebuild -license accept
  /usr/bin/xcodebuild -runFirstLaunch
fi
echo "selected: $(/usr/bin/xcode-select -p)"

# AND THE DISK IT WAS EXPANDED INTO IS REMOVED, because it is 15 GiB and nothing reads it again.
rm -rf "$WORK"
