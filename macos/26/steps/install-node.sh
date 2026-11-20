#!/bin/bash
# NODE FROM nodejs.org, x64 .pkg. The official installer puts it in
# /usr/local/bin and links node/npm/npx there, which is on the default PATH.
set -uo pipefail
if command -v node >/dev/null 2>&1; then
  echo "node is already installed: $(node --version)"
  exit 0
fi
PKG=/tmp/node.pkg
if [ ! -f "$PKG" ]; then
  echo "=== NO NODE PACKAGE AT $PKG ==="
  exit 1
fi
export PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin
printf '%s\n' '{{ password }}' | sudo -S -p '' installer -pkg "$PKG" -target / 2>&1 | tail -5
rm -f "$PKG"
hash -r 2>/dev/null || true
command -v node && node --version && npm --version
