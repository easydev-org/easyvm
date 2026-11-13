#!/bin/sh
# RUN ONE LEG ON THE POD, WITH EVERY DISK STAYING ON THE PVC.
#
#   k8s/lab-run.sh ubuntu install
#   k8s/lab-run.sh ubuntu ssh        # continues from the disk install wrote
#
# NO REGISTRY ANYWHERE. This is the local loop, and its whole point is that a leg's product is a FILE
# on the PVC rather than a published artifact:
#
#	/disks/os/<os>/install.qcow2      what the install leg wrote
#	/disks/os/<os>/ssh.qcow2          what the ssh leg wrote, on top of install's disk
#
# A LEG THAT CONTINUES ONE STARTS FROM THE PREVIOUS FILE WITH AN OVERLAY, which `boot:` already does
# when it is handed a disk and asked to run on top of it: the parent stays read-only and the diff is
# thrown away unless this leg publishes to its own name. That is the same mechanism CI uses across
# `oras` - only the transport is absent, so a failure here cannot be a registry failure.
#
# THE YAML IS THE REPOSITORY'S, UNCHANGED. Nothing in /walks is edited for this: the file still says
# `fetch: linux_iso` and `compress: {{ out }}`, and what changes is where the files are.
set -eu

os=${1:?os}
leg=${2:-install}

case "$leg" in
    install|setup|gbi) prev="" ;;
    ssh)               prev=install ;;
    vm|vm-configure)   prev=ssh ;;
    dev)               prev=vm-configure ;;
    *)                 prev="" ;;
esac

# THE WALKTHROUGH NAME IS NOT THE LEG NAME for three of the four: ghostbsd installs through `gbi`,
# macOS through `setup`, and the rest through `install`.
walk=""
for candidate in "$leg" install gbi setup; do
    if [ -f "/walks/$os-$candidate.yaml" ]; then walk="/walks/$os-$candidate.yaml"; break; fi
done
[ -n "$walk" ] || { echo "no walkthrough for $os/$leg; /walks holds:" >&2; ls /walks >&2; exit 2; }

dir="/disks/os/$os"
run="$dir/run-$leg"
mkdir -p "$dir"

# COMPLETELY RESET, EVERY TIME. The run directory, the daemon, and the tap the last run left behind -
# so a rerun cannot be polluted by its own previous frames, overlay or network device.
echo "=== reset $run"
pkill easyvm 2>/dev/null || true
sleep 1
ip link del qemutap0 2>/dev/null || true
rm -rf "$run"
mkdir -p "$run"

# THE PARENT DISK, WHEN THIS LEG CONTINUES ONE. It is the PREVIOUS leg's output on this PVC; there is
# no pull, because the file is already here.
boot=""
if [ -n "$prev" ]; then
    for f in "$dir/$prev.qcow2" "$dir/install.qcow2" "$dir/setup.qcow2" "$dir/gbi.qcow2"; do
        [ -f "$f" ] && { boot="$f"; break; }
    done
    [ -n "$boot" ] || { echo "=== NO DISK TO CONTINUE: $dir has no $prev.qcow2" >&2; ls -la "$dir" >&2; exit 1; }
    echo "=== continuing from $boot"
    cp "$boot" "$run/disk.qcow2"
else
    echo "=== a fresh disk"
fi

# AN INSTALLER IMAGE FROM THE ARTIFACT PVC, COPIED IN AS `installer.iso` so `fetch:` finds it first and
# never reaches a registry. The alias matters: the walkthrough says `release: ubuntu` and the file on
# the PVC is `ubuntu.iso`.
if [ -z "$prev" ]; then
    # TWO PLACES, AND THE ORDER IS DELIBERATE. /disks/isos is this PVC's own earlier download and it is
    # already local to the node; /artifacts/isos is the shared artifact PVC filled from ghcr. Checking
    # both means a file downloaded once by either route is not downloaded again.
    for base in /disks/isos /artifacts/isos; do
        found=""
        for name in "$os" "$os-desktop" "$os-mate" "$os-xfce" "$os-core" "$os-graphical" "$os-kde" \
                    "$os-leap-16.1" "$os-workstation" "$os-cinnamon"; do
            if [ -f "$base/$name.iso" ]; then found="$base/$name.iso"; break; fi
        done
        if [ -n "$found" ]; then
            echo "=== installer: $found"
            cp "$found" "$run/installer.iso"
            break
        fi
    done
fi

echo "=== daemon"
easyvm serve --work "$run" > "$run/daemon.log" 2>&1 &
i=0
while [ $i -lt 40 ]; do
    [ -S "$run/easyvm.sock" ] && break
    i=$((i + 1)); sleep 1
done
[ -S "$run/easyvm.sock" ] || { echo "the daemon never listened" >&2; tail -20 "$run/daemon.log" >&2; exit 1; }

echo "=== $walk"
set +e
easyvm run "$walk" \
    --work "$run" \
    --boot-disk "$run/disk.qcow2" \
    --out "$dir/$leg.qcow2" \
    --password admin \
    > "$run/run.log" 2>&1
rc=$?
set -e

echo "=== finished rc=$rc"
tail -25 "$run/run.log"
echo "=== products"
ls -la "$dir"/*.qcow2 "$run"/*.mp4 2>/dev/null | awk '{printf "  %-46s %6d MiB\n", $9, $5/1048576}' || true
exit $rc
