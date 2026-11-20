#!/bin/bash
# PYTHON FROM python.org, macos11 .pkg - the Intel-capable build. It installs into
# /Library/Frameworks/Python.framework and links python3 into /usr/local/bin.
set -uo pipefail
if /usr/local/bin/python3 -c 'import sys; print(sys.version)' >/dev/null 2>&1; then
  echo "python3 is already installed: $(/usr/local/bin/python3 --version)"
  exit 0
fi
PKG=/tmp/python.pkg
if [ ! -f "$PKG" ]; then
  echo "=== NO PYTHON PACKAGE AT $PKG ==="
  exit 1
fi
export PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin
printf '%s\n' '{{ password }}' | sudo -S -p '' installer -pkg "$PKG" -target / 2>&1 | tail -5
rm -f "$PKG"
hash -r 2>/dev/null || true
/usr/local/bin/python3 --version && /usr/local/bin/pip3 --version
