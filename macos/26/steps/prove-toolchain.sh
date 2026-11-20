#!/bin/bash
# THE CHECK IS THE PATH, NOT THE VERSION.
#
# /usr/bin/python3 exists on every macOS and answers --version perfectly - it is the one
# Xcode's command line tools install, and its pip refuses to write. A check that only
# read a version would pass on a disk whose python cannot install anything.
#
# WITH HOMEBREW OUT, THE EXPECTED PREFIX IS DIFFERENT: node and python come from
# publishers' .pkg files and land in /usr/local, while git and the compilers come from
# Xcode and live under /Applications/Xcode.app or /usr/bin.
set -uo pipefail
export PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin
bad=0

# FROM THE PUBLISHERS' PACKAGES: /usr/local.
for t in node npm python3 pip3; do
  path=$(command -v "$t" 2>/dev/null || true)
  if [ -z "$path" ]; then
    echo "MISSING  $t"
    bad=1
    continue
  fi
  case "$path" in
    /usr/local/*) echo "ok       $t -> $path" ;;
    *) echo "WRONG    $t -> $path (not from /usr/local)"; bad=1 ;;
  esac
done

# FROM XCODE'S COMMAND LINE TOOLS: /usr/bin, but only after the dev dir is selected.
for t in git clang swift; do
  path=$(command -v "$t" 2>/dev/null || true)
  if [ -z "$path" ]; then
    echo "MISSING  $t"
    bad=1
  else
    echo "ok       $t -> $path"
  fi
done

if [ "$bad" -ne 0 ]; then
  echo "=== THE TOOLCHAIN IS NOT COMPLETE ==="
  exit 1
fi
node --version
python3 --version
git --version
echo "TOOLCHAIN_OK"
