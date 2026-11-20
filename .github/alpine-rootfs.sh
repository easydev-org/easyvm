#!/bin/sh
# BUILD THE ALPINE ROOT FILESYSTEM A MICROVM BOOTS.
#
# A microvm has NO FIRMWARE AND NO INSTALLER: qemu is handed a kernel, an initramfs and a disk. So the
# disk has to arrive ready, and this is what makes it. Nothing here installs Alpine - `apk` populates
# a directory and `mke2fs -d` writes it to an image, which is the same shape Firecracker's own
# getting-started guide uses.
#
# WHY NOT THE NETBOOT ARTIFACTS ALONE. Alpine publishes `vmlinuz-virt` and `initramfs-virt` for a
# DISKLESS guest: the initramfs mounts a squashfs `modloop`, keeps the root in tmpfs and asks the
# network for packages. MEASURED with those alone on a microvm:
#
#	Mounting boot media: ok.
#	Installing packages to root filesystem: OK: 0 MiB in 0 packages
#	/sbin/init not found in new root. Launching emergency recovery shell
#
# because there is no network in that machine and nothing was ever on disk.
#
# THE THREE OUTPUTS THIS PRODUCES, AND WHY EACH IS NEEDED:
#
#	alpine-vmlinuz     the kernel. Alpine ships it WITHOUT a version in the name - `vmlinuz-virt` -
#	                   which is why a path like `vmlinuz-6.12.x` fails with "No such file".
#	alpine-initramfs   REBUILT, because the stock one has 72 modules and ext4 is not among them. A
#	                   guest whose root is ext4 answers `mount: /dev/vda: No such device`. `mkinitfs`
#	                   with the ext4 feature is what fixes it.
#	alpine.qcow2       the root filesystem, as an ext4 image converted to qcow2. QCOW2 AND NOT RAW
#	                   because the engine boots every machine on an OVERLAY over the disk, and
#	                   `qemu-img create -b` refuses a raw backing file.
#
# AND `tty1` GETS A GETTY. That is what puts a LOGIN PROMPT ON THE VGA ADAPTER - the difference
# between an mp4 of a guest and an mp4 of a black field. Without it the guest is healthy and the video
# is empty, which is the failure this file exists to remove.
set -eu

V="${ALPINE_VERSION:-3.21}"
BASE="${ALPINE_MIRROR:-https://dl-cdn.alpinelinux.org/alpine}/v${V}"
OUT="${1:-.}"
WORK="${OUT}/alpine-build"

log() { printf '  %s\n' "$*"; }

# THE RUNNER IS ALREADY ROOT AND HAS NO `sudo`. MEASURED: the first CI run of this script died with
# `/__w/easyvm/easyvm/.github/alpine-rootfs.sh: 62: sudo: not found`. A developer machine may need it,
# so it is used only when it is there - and never for a command that is not chroot/NEWROOT.
if [ "$(id -u)" -eq 0 ]; then
	as_root() { "$@"; }
elif command -v sudo >/dev/null 2>&1; then
	as_root() { sudo "$@"; }
else
	log "not root and no sudo: apk/mkinitfs/mke2fs will fail"
	as_root() { "$@"; }
fi

mkdir -p "$WORK" "$OUT"

# --- 1. fetch -----------------------------------------------------------------------------
if [ ! -f "$WORK/minirootfs.tar.gz" ]; then
	log "fetching alpine-minirootfs-${V}"
	curl -fsSL --retry 3 --max-time 300 \
		-o "$WORK/minirootfs.tar.gz" \
		"${BASE}/releases/x86_64/alpine-minirootfs-${V}.0-x86_64.tar.gz"
fi

# --- 2. populate a root tree --------------------------------------------------------------
if [ ! -d "$WORK/root/usr" ]; then
	log "unpacking and installing the packages"
	rm -rf "$WORK/root"
	mkdir -p "$WORK/root"
	tar -xzf "$WORK/minirootfs.tar.gz" -C "$WORK/root"

	# THE MIRROR IS WRITTEN EXPLICITLY so a pinned `ALPINE_VERSION` cannot silently fall back to
	# whatever the minirootfs shipped configured.
	printf '%s/main\n%s/community\n' "$BASE" "$BASE" > "$WORK/root/etc/apk/repositories"
	as_root cp /etc/resolv.conf "$WORK/root/etc/resolv.conf"
	# AN APK THAT RUNS INSIDE chroot DOES NOT INHERIT THIS SHELL'S PROXY, and a runner behind one sees
	# `fetch ...: temporary failure in name resolution`. The variables are passed explicitly so a
	# direct-connect runner is unaffected.


	# linux-virt BRINGS THE KERNEL AND /lib/modules, which is what `mkinitfs` reads. The X packages
	# are what make this a DESKTOP rather than a serial console: openbox is the window manager, and
	# xf86-video-vesa is the driver a microvm's ISA VGA adapter answers to.
	# THE PROXY IS PASSED INTO THE CHROOT, because `apk` reads it from the ENVIRONMENT and a
	# `/etc/profile.d` file is only sourced by a login shell. MEASURED: without this a runner behind a
	# proxy answers "temporary failure in name resolution" and installs 9 MiB of nothing.
	as_root env ${https_proxy:+https_proxy="$https_proxy"} ${http_proxy:+http_proxy="$http_proxy"} \
		chroot "$WORK/root" /sbin/apk add --no-cache \
		alpine-base linux-virt mkinitfs \
		openbox xorg-server xf86-input-libinput xf86-video-vesa \
		xinit xterm font-misc-misc
	log "root tree: $(du -sm "$WORK/root" | cut -f1) MiB"
fi

ROOT="$WORK/root"
KVER=$(ls "$ROOT/lib/modules" | head -1)

# --- 3. rebuild the initramfs with ext4 ----------------------------------------------------
# WITHOUT THIS STEP THE GUEST CANNOT MOUNT ITS OWN ROOT. The stock netboot initramfs carries 72
# modules and `squashfs` is one of them while `ext4` is not - measured by listing the cpio members.
if [ ! -f "$WORK/initramfs-ext4" ]; then
	log "rebuilding the initramfs with ext4 (kernel $KVER)"
	grep -q 'ext4' "$ROOT/etc/mkinitfs/mkinitfs.conf" 2>/dev/null || {
		as_root sed -i 's/^features="\(.*\)"/features="\1 ext4 virtio_blk virtio_pci ata_piix sd_mod"/' \
			"$ROOT/etc/mkinitfs/mkinitfs.conf"
	}
	as_root chroot "$ROOT" /sbin/mkinitfs -o /boot/initramfs-ext4 "$KVER"
	as_root cp "$ROOT/boot/initramfs-ext4" "$WORK/initramfs-ext4"
fi

# --- 4. make the guest draw on the VGA adapter --------------------------------------------
# THE LOGIN PROMPT IS THE PICTURE. `tty1` is the adapter's console and `ttyS0` is the serial line; a
# getty on BOTH means the walkthrough can assert on the console (which is reliable) while the
# recording has something to show (which is the artifact).
log "writing inittab: a getty on tty1 and ttyS0"
as_root tee "$ROOT/etc/inittab" >/dev/null <<'EOF'
::sysinit:/sbin/openrc sysinit
::sysinit:/sbin/openrc boot
::wait:/sbin/openrc default
::ctrlaltdel:/sbin/reboot
::shutdown:/bin/umount -a -r
tty1::respawn:/sbin/getty -L tty1 38400 vt100
ttyS0::respawn:/sbin/getty -L ttyS0 115200 vt100
EOF

# --- 5. write the disk --------------------------------------------------------------------
# `mke2fs -d` WRITES A DIRECTORY INTO AN IMAGE WITHOUT A LOOP DEVICE, which is what a container
# allows. This is the same step Firecracker's getting-started guide uses (`mkfs.ext4 -d`).
if [ ! -f "$WORK/rootfs.raw" ]; then
	SIZE=$(( $(as_root du -sm "$ROOT" | cut -f1) + 120 ))
	log "writing a ${SIZE} MiB ext4 image"
	rm -f "$WORK/rootfs.raw"
	truncate -s "${SIZE}M" "$WORK/rootfs.raw"
	as_root mke2fs -q -F -t ext4 -d "$ROOT" "$WORK/rootfs.raw"
fi

log "converting to qcow2 (the engine boots every machine on an overlay, which needs qcow2)"
qemu-img convert -f raw -O qcow2 -c "$WORK/rootfs.raw" "$OUT/alpine.qcow2"

# --- 6. the two files the boot step names -------------------------------------------------
cp "$ROOT/boot/vmlinuz-virt" "$OUT/alpine-vmlinuz"
cp "$WORK/initramfs-ext4" "$OUT/alpine-initramfs"

log "done:"
ls -l "$OUT/alpine-vmlinuz" "$OUT/alpine-initramfs" "$OUT/alpine.qcow2"
