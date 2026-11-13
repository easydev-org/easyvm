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
# THE FILE THE WORKFLOW USES, BY ITS OWN MAPPING.
#
# `ghostbsd-install` IS NOT THE INSTALLER. It is a legacy file that boots an ALREADY-BUILT disk, while
# the workflow's install leg runs `gbi` - which is the one with `create the empty disk` and `fetch:`.
# Picking by name took the wrong file and the failure was
#
#	boot: no the target disk at .../disk.qcow2: no such file or directory
#
# SO THE MAPPING IS WRITTEN DOWN HERE, matching each workflow's `file:` entry. A leg that is not
# listed falls back to its own name, which is right for ubuntu, windows and the rest.
case "$leg" in
    install) case "$os" in
                 ghostbsd) walk=/walks/ghostbsd-gbi.yaml ;;
                 macos)    walk=/walks/macos-setup.yaml ;;
                 *)        walk="/walks/$os-install.yaml" ;;
             esac ;;
    setup)   walk=/walks/macos-setup.yaml ;;
    ssh)     walk="/walks/$os-ssh.yaml" ;;
    vm*)     walk="/walks/$os-vm-configure.yaml" ;;
    dev)     walk="/walks/$os-dev.yaml" ;;
    *)       walk="/walks/$os-$leg.yaml" ;;
esac

[ -n "$walk" ] || { echo "no walkthrough for $os/$leg; /walks holds:" >&2; ls /walks >&2; exit 2; }

dir="/disks/os/$os"
run="$dir/run-$leg"
# THE DIRECTORY HAS TO EXIST BEFORE THE LOG IS REDIRECTED, and on a fresh PVC it does not - the
# launcher writes `> /disks/os/<os>-<leg>.log` before this script runs.
mkdir -p "$dir" /disks/os

# COMPLETELY RESET, EVERY TIME. The run directory, the daemon, and the tap the last run left behind -
# so a rerun cannot be polluted by its own previous frames, overlay or network device.
echo "=== reset $run"
# WAIT FOR THE OLD DAEMON TO ACTUALLY BE GONE, BECAUSE `pkill` ONLY ASKS.
#
# MEASURED, and it produced a machine that ran with NO BROWSER PAGE:
#
#	[easyvm] http surface stopped: http listen :8080: bind: address already in use
#
# The previous daemon was still closing when this one started, so it took the socket, bound 8080
# first, and the new daemon's HTTP surface died - while its qemu started normally. The run then looked
# healthy from `status` and `screen.png` was a 000 from outside, which reads as a network problem.
#
# `pkill` IS A REQUEST AND THE DAEMON HAS WORK TO DO ON THE WAY OUT (stop the guest, close the socket,
# close the page), so the wait is on the PROCESS, not on a fixed second.
pkill easyvm 2>/dev/null || true
# A ZOMBIE IS NOT A RUNNING DAEMON, AND `pgrep` CANNOT TELL THE DIFFERENCE.
#
# MEASURED: this container's PID 1 is `sleep infinity`, which never reaps anything, so every daemon
# that has exited stays in the table as `<defunct>`. `pgrep -x easyvm` matched fourteen of them and
# this loop waited the full thirty seconds for processes that were already gone - then `pkill -9`ed
# nothing. The check has to ask whether any easyvm is in a NON-ZOMBIE state.
daemons_alive() {
    ps -eo stat=,cmd= 2>/dev/null | awk '$1 !~ /^Z/ && $2 ~ /easyvm/ { found = 1 } END { exit !found }'
}
i=0
while daemons_alive && [ $i -lt 30 ]; do
    i=$((i + 1))
    sleep 1
done
if daemons_alive; then
    echo "=== the previous daemon did not exit in 30s; killing it" >&2
    pkill -9 easyvm 2>/dev/null || true
    sleep 2
fi
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

# EVERY ASSET IS SEEDED FROM THE PVC UNDER THE NAME THE FETCH STEP LOOKS FOR.
#
# `fetch:` READS THE WORK DIRECTORY FIRST AND ONLY REACHES A REGISTRY WHEN THE FILE IS ABSENT, so a
# pre-seeded PVC means the local loop never pulls. THE NAMES ARE THE FETCH LAYER'S OWN - measured from
# `internal/asset` and `internal/opencore`, not guessed:
#
#	fetch: linux_iso         work/installer.iso
#	fetch: windows_iso       work/windows11.iso
#	fetch: virtio_win        work/virtio-win.iso
#	fetch: recovery_macos    work/recovery.dmg
#	fetch: opencore          work/opencore.zip + opencore-template.iso + macos.rom
#	                                     + OVMF_VARS-1920x1080.fd
#
# THE MACOS FIVE WERE MISSING, AND THE COST WAS A REGISTRY PULL: the pod sat on `fetch the recovery
# image` at 888 MiB while /artifacts/macos held the same file, because nothing copied it in.
if [ "$os" = macos ]; then
    for pair in \
        "recovery-macos.dmg:recovery.dmg" \
        "opencore.zip:opencore.zip" \
        "opencore-template.iso:opencore-template.iso" \
        "OVMF_CODE.fd:macos.rom" \
        "OVMF_VARS-1920x1080.fd:OVMF_VARS-1920x1080.fd"
    do
        src="/artifacts/macos/${pair%%:*}"
        dst="$run/${pair##*:}"
        if [ -f "$src" ]; then
            echo "=== macos asset: $(basename "$src") -> $(basename "$dst")"
            cp "$src" "$dst"
        fi
    done
fi

# WINDOWS NAMES ITS IMAGE `windows11.iso`, NOT `installer.iso`. `fetch: windows_iso` looks for that
# fixed name in the work directory, so the artifact is copied under BOTH names and whichever the
# step asks for is already there.
if [ "$os" = windows ]; then
    for base in /disks/isos /artifacts/isos; do
        if [ -f "$base/windows11.iso" ]; then
            echo "=== windows image: $base/windows11.iso"
            cp "$base/windows11.iso" "$run/windows11.iso"
            cp "$base/virtio-win.iso" "$run/virtio-win.iso" 2>/dev/null || true
            break
        fi
    done
fi

# THE WALKTHROUGH NAMES FILES RELATIVE TO THE REPOSITORY - `template: windows/11/answer/Autounattend.xml`
# is a path in the repo, not in the work directory - so the daemon is started from a tree that has that
# layout. /repo-src is a copy of the repository's own directories, put on the PVC once so it survives
# pod recreation.
if [ -d /disks/repo-src/windows ]; then
    echo "=== working from /repo-src (the repository's layout)"
    cd /disks/repo-src
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
