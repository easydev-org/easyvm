#!/bin/bash
set -uo pipefail
work=$(mktemp -d)
cat > "$work/hello.swift" <<'SWIFT'
print("hello-from-swift")
SWIFT
if ! swiftc "$work/hello.swift" -o "$work/hello"; then
  echo "SWIFT_COMPILE_FAILED"
  exit 1
fi
"$work/hello" || exit 1
echo "SWIFT_OK"
rm -rf "$work"
