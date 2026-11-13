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
echo "=== reset the machine slot"

# THIS SCRIPT DOES NOT START A DAEMON, BECAUSE THE POD HAS ONE.
#
# `k8s/lab.yaml` runs `easyvm serve --work /work` as PID 1 with `restartPolicy: Always`, and ONE
# DAEMON RUNS EVERY LEG - `destroy` stops the machine and keeps the server:
#
#	func (d *Daemon) destroySandbox() Response {
#		closeFn()                      // the guest goes
#		d.st.Status = "destroyed"      // the daemon stays
#	}
#
# so install, then ssh, then vm-configure are three `run` requests to the same process, on one socket,
# with one owner for the frames and the ports. THE VERSION THAT STARTED A DAEMON PER LEG is what put
# two of them in one pod - the second bound 8080 while the first still held 6080 - and every `pkill`
# in it was cleaning up after a supervision model that did not exist.
# A RUN IN PROGRESS HAS TO BE STOPPED, AND `destroy` DOES NOT DO IT.
#
# `destroy` ends a SANDBOX - the machine `create` leaves running - and returns
#
#	REFUSED: a run is already in progress (step "the installer finishes"); stop it first
#
# for a `run`, which is what every leg here is. Measured: two legs "restarted" while the first was still
# compressing a disk, and the second was refused, so the pod kept running the OLD file and the fix under
# test was never exercised at all.
#
# SO THE ORDER IS `stop` THEN `destroy`: stop ends the walk and its machine, destroy clears anything a
# `create` left. Both are best-effort, because on a fresh pod there is nothing to stop.
echo "=== stop the run in progress, if any"
easyvm stop --work /work >/dev/null 2>&1 || true

echo "=== destroy whatever a create left behind"
easyvm destroy --work /work >/dev/null 2>&1 || true

# THE PORTS, BECAUSE A DESTROYED MACHINE'S LISTENERS CLOSE AFTER ITS QEMU IS REAPED. A boot that starts
# in that gap fails with
#
#	boot: port 6080 is already in use, so the noVNC page cannot be served
wait_port_free() {
    port=$1
    i=0
    while [ $i -lt 20 ]; do
        ss -lnt 2>/dev/null | grep -q ":$port " || return 0
        i=$((i + 1))
        sleep 1
    done
    return 1
}
# THE PORT AND THE PROCESS ARE TWO DIFFERENT FACTS. A `stop` asks qemu to quit; a qemu still in `Rl`
# holds 5900 with nothing listening behind it, and the next boot dies with
#
#	qemu-system-x86_64: -vnc 127.0.0.1:0: Failed to find an available port: Address already in use
#
# MEASURED: port 5900 LISTENing, qemu pid 153 alive, `destroy` reporting success.
qemu_alive() {
    ps -eo stat=,cmd= 2>/dev/null | awk '$1 !~ /^Z/ && $2 ~ /qemu-system/ { f = 1 } END { exit !f }'
}
i=0
while qemu_alive && [ $i -lt 30 ]; do
    i=$((i + 1)); sleep 1
done
if qemu_alive; then
    echo "=== a qemu outlived its run; killing it" >&2
    pkill -9 -f "qemu-system-x86_64" 2>/dev/null || true
    sleep 2
fi
for port in 5900 8080; do
    wait_port_free "$port" || echo "=== port $port is still held after 20s" >&2
done

# THE TAP IS A HOST DEVICE, NOT A PROCESS'S, so it outlives a machine that was killed. One guest per
# container means one name, and a stale one makes qemu refuse to start.
ip link del qemutap0 2>/dev/null || true

# THE LEG'S OWN SCRATCH IS THE DAEMON'S WORK DIRECTORY, because that is where its socket, frames and
# overlay live - and there is exactly one of those per daemon. What belongs to the LEG and must
# survive is the disk and the video, and those go to the PVC.
# THE PARENT DISK, WHEN THIS LEG CONTINUES ONE. It is the PREVIOUS leg's output on this PVC; there is
# no pull, because the file is already here.
boot=""
if [ -n "$prev" ]; then
    for f in "$dir/$prev.qcow2" "$dir/install.qcow2" "$dir/setup.qcow2" "$dir/gbi.qcow2"; do
        [ -f "$f" ] && { boot="$f"; break; }
    done
    [ -n "$boot" ] || { echo "=== NO DISK TO CONTINUE: $dir has no $prev.qcow2" >&2; ls -la "$dir" >&2; exit 1; }
    echo "=== continuing from $boot"
    cp "$boot" "/work/disk.qcow2"
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
            cp "$found" "/work/installer.iso"
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
        dst="/work/${pair##*:}"
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
            cp "$base/windows11.iso" /work/windows11.iso
            cp "$base/virtio-win.iso" /work/virtio-win.iso 2>/dev/null || true
            break
        fi
    done

    # THE ANSWER FILE IS A RELATIVE PATH IN THE WALKTHROUGH, AND THE DAEMON'S CWD IS `/`.
    #
    #	- name: build the answer CD
    #	  template: windows/11/answer/Autounattend.xml
    #
    # `cd` in this script changes THIS SHELL, not the daemon - the daemon is PID 1 and started with
    # its own working directory - so the step failed with
    #
    #	build the answer CD: no answer file at windows/11/answer/Autounattend.xml
    #
    # IN CI THAT PATH RESOLVES BECAUSE THE DAEMON RUNS FROM THE CHECKOUT. Here the repository's own
    # directories live under /disks/repo-src, so the SAME RELATIVE LAYOUT is reproduced under /work -
    # the file the file names, at the path the file names, which is the property that matters.
    if [ -f /disks/repo-src/windows/11/answer/Autounattend.xml ]; then
        mkdir -p /work/windows/11/answer
        cp /disks/repo-src/windows/11/answer/Autounattend.xml /work/windows/11/answer/
        echo "=== answer file -> /work/windows/11/answer/Autounattend.xml"
    fi
fi

# THE WALKTHROUGH NAMES FILES RELATIVE TO THE REPOSITORY - `template: windows/11/answer/Autounattend.xml`
# is a path in the repo, not in the work directory - so the daemon is started from a tree that has that
# layout. /repo-src is a copy of the repository's own directories, put on the PVC once so it survives
# pod recreation.
if [ -d /disks/repo-src/windows ]; then
    echo "=== working from /repo-src (the repository's layout)"
    cd /disks/repo-src
fi

echo "=== $walk"
set +e
easyvm run "$walk" \
    --work /work \
    --boot-disk /work/disk.qcow2 \
    --out "$dir/$leg.qcow2" \
    --password admin \
    > "$run/run.log" 2>&1
rc=$?
set -e

echo "=== finished rc=$rc"
tail -25 "$run/run.log"
echo "=== products"
# THE VIDEO IS IN THE DAEMON'S WORK DIRECTORY, so it is moved beside the disk the leg produced. One
# place to look for "what did this leg make" is worth the copy, and the PVC is what survives the pod.
for f in /work/*.mp4; do
    [ -f "$f" ] && cp "$f" "$run/" 2>/dev/null
done
ls -la "$dir"/*.qcow2 "$run"/*.mp4 2>/dev/null | awk '{printf "  %-46s %6d MiB\n", $9, $5/1048576}' || true
exit $rc
