#!/bin/sh
# RUN ONE WALKTHROUGH FROM A CLEAN SLATE, ON THE POD'S PVC.
#
#   k8s/run.sh ubuntu install
#   k8s/run.sh ghostbsd install
#   k8s/run.sh windows install
#   k8s/run.sh macos setup
#
# THE RESET IS THE POINT. Every invocation wipes the run directory, kills any daemon still holding
# the QMP socket, and removes the tap the previous run left behind - so a rerun cannot be polluted by
# its own last disk, overlay, frames or stale network device. "From the beginning, keeping nothing" is
# a property of THIS script, not something the caller has to remember.
#
# THE YAML COMES FROM THE CONFIGMAP. /walks/<os>/<leg>.yaml is the file the repository holds; nothing
# is rewritten for the local case, so a step that fails here fails in CI for the same reason.
set -eu

os=${1:-}
leg=${2:-install}
[ -n "$os" ] || { echo "usage: run.sh <os> [leg]" >&2; exit 2; }

walk="/walks/$os/$leg.yaml"
[ -f "$walk" ] || { echo "no walkthrough at $walk" >&2; ls -R /walks | head -20 >&2; exit 2; }

run="/disks/run-$os-$leg"

echo "=== resetting $run"
pkill easyvm 2>/dev/null || true
sleep 1
ip link del qemutap0 2>/dev/null || true
rm -rf "$run"
mkdir -p "$run"

# AN INSTALLER IMAGE ALREADY ON THE PVC IS USED AS-IS. `fetch:` looks here first, so seeding
# /disks/isos/<name>.iso once means every later run is offline. A missing one is reported by the
# fetch step itself, with the registry it would have tried.
for seed in /disks/isos/*.iso; do
    [ -e "$seed" ] || continue
    cp -n "$seed" "$run/installer.iso" 2>/dev/null || true
done

echo "=== daemon"
easyvm serve --work "$run" > "$run/daemon.log" 2>&1 &
i=0
while [ $i -lt 30 ]; do
    [ -S "$run/easyvm.sock" ] && break
    i=$((i + 1))
    sleep 1
done
[ -S "$run/easyvm.sock" ] || { echo "the daemon never listened" >&2; tail -20 "$run/daemon.log" >&2; exit 1; }

echo "=== $walk"
easyvm run "$walk" \
    --work "$run" --socket "$run/easyvm.sock" \
    --boot-disk /disks/run-$os-$leg/disk.qcow2 \
    --out "$run/out.qcow2" \
    ${USER_NAME:+--user "$USER_NAME"} \
    ${USER_PASS:+--password "$USER_PASS"} \
    > "$run/run.log" 2>&1
rc=$?

echo "=== finished rc=$rc"
tail -30 "$run/run.log"
ls -la "$run"/*.mp4 "$run"/out.qcow2 2>/dev/null || true
exit $rc
