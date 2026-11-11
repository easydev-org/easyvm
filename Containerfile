# easyvm's image, built from a bare Debian rather than from someone else's VM image.
#
# WHY NOT FROM qemux/qemu
#
# That image is debian:trixie-slim plus 33 apt packages and a Go entrypoint that configures a VM
# from environment variables (BOOT=, DISK_SIZE=, VNC=). Measured from its own Dockerfile on master:
# qemu comes from a Debian *sid snapshot* pinned to 1:11.1.0+ds-2, and the rest is a supervisor for
# its own web UI - nginx, tini, dnsmasq for its own DHCP, websocketd, a passt build, a boot logo.
#
# NONE OF THAT IS USED HERE. easyvm builds its own qemu command line, serves its own console over
# net/http (see internal/daemon/console.go), and creates its own bridge, tap, DHCP, NAT and DNAT
# (internal/net). Inheriting the image would mean shipping a second, unused VM supervisor and a
# second dnsmasq that nothing starts.
#
# WHAT IS KEPT FROM IT: the qemu VERSION. 11.1.0 is what every measurement in easydiskbake was
# taken with, including `-cpu Skylake-Client-v4`. Debian's own trixie package is older. The next
# section pins it the same way and for the same reason.
FROM debian:trixie-slim

ARG DEBIAN_FRONTEND=noninteractive
ARG DEBCONF_NOWARNINGS=yes

# QEMU AND OVMF AT THE VERSIONS THE MEASUREMENTS USED.
#
# THE PINNED SNAPSHOT IS NOT DECORATION. `-cpu Skylake-Client-v4` is the one CPU model every guest
# starts with, and the guest behaviours this repository recorded - a Desktop that takes 36 seconds,
# a TLS handshake that fails over a 1450-byte link - were measured against qemu 11.1. A container
# with a different qemu is a different machine, and the difference shows up as a stage that passes
# locally and fails in CI for no reason anyone can see.
#
# THE ASSERTION AT THE END OF THIS FILE IS WHAT MAKES THE PIN REAL. A version number in a comment
# is a hope; `qemu-system-x86_64 -cpu help | grep` is a fact, and it fails the build rather than a
# walkthrough.
ARG VERSION_QEMU="1:11.1.0+ds-2"
ARG DEBIAN_SNAPSHOT="20260819T142328Z"

# WHAT IS INSTALLED, AND WHY EACH PACKAGE IS HERE
#
# THE MACHINE
#   qemu-system-x86   the guest, and QMP - which is how everything on the screen is driven
#   qemu-utils        qemu-img: create, convert, compress, and `check` a disk before publishing it
#   ovmf              UEFI firmware. Windows 11 will not start on SeaBIOS, and a guest with no UEFI
#                     boots to "no bootable device" - which reads as a broken disk rather than a
#                     missing package.
#   swtpm             a software TPM 2.0. A Windows disk is sealed against the TPM it was installed
#                     with and will not boot without one.
#
# THE NETWORK. THERE IS ONE MODE AND IT NEEDS ALL FOUR.
#
#   iproute2          `ip tuntap`, `ip link ... type bridge`, `ip link set master` - the tap and
#                     the bridge. qemu's `-netdev user` is NOT used and cannot be: the host's smbd
#                     listens on the bridge address, and a user-mode guest is on 10.0.2.0/24.
#   iptables          MASQUERADE for outbound, FORWARD ACCEPT for the one guest pair, and DNAT for
#                     the published ports. DNAT is why `ssh 172.30.0.1:2222` reaches the guest's 22
#                     without qemu being involved at all.
#   dnsmasq           DHCP. A bridge hands out nothing; the guest sends DISCOVER and something has
#                     to answer. The lease is by MAC, which is what makes 172.30.0.2 a fact before
#                     any guest exists.
#   procps            `sysctl`: net.ipv4.ip_forward=1, and IPv6 disabled per interface.
#
# THE SHARE
#   samba-common-bin  smbd, and NOT the full `samba` metapackage: Winbind and the AD tooling are
#                     for a domain member and this is a standalone share. The server is host-side on
#                     purpose - `-netdev user,smb=` makes qemu fork its own smbd, which is a second
#                     server with a second config, and telling the two apart cost several runs.
#
# THE VERDICT
#   ffmpeg            frames to a video. A recording is the one artefact that shows an installer
#                     doing something no log line mentions.
#   tesseract-ocr     `expect:` and `until:` read text off the screen.
#   tesseract-ocr-eng ...and the language pack: without it tesseract runs, exits zero and reads
#                     nothing, which looks exactly like a blank screen.
#
# THE DISK TOOLS
#   e2fsprogs         debugfs: read a file out of a finished ext4 disk without mounting it.
#   xorriso, genisoimage  rebuild an ISO with an answer file at its root.
#   mtools            mformat/mcopy/mdir: build a FAT image without loop-mounting, which a
#                     container has no privileges to do.
#   p7zip-full        `7z`, for the macOS OpenCore template: an ISO9660 image with a Joliet tree,
#                     and the names live in the Joliet layer.
#   aria2             segmented downloader. An 847 MiB transfer from Apple's CDN gets reset around
#                     20 MiB on one connection.
#   zstd              `unzstd`, for a vendor image published as `.img.zst`. NOT for qcow2: that is
#                     compression_type=zstd through qemu-img, which needs no binary.
#   wimtools          wimlib-imagex: read a Windows ISO's install.wim metadata without Windows.
#
# DELIBERATELY ABSENT
#   python3, python3-pip, qemu.qmp   the implementation is Go; it speaks QMP directly
#   nginx, websocketd               internal/daemon serves the noVNC page and bridges its WebSocket
#   tini                            the daemon is not PID 1 and does not need an init shim
#   net-tools, iputils-ping, ipcalc, ethtool, netcat-openbsd
#                                   none of these is called anywhere in the Go code, and each was
#                                   named by qemux's image for its own purposes
RUN set -eux; \
    # non-free is where some qemu firmware blobs live; the snapshot below is official Debian.
    echo "deb https://deb.debian.org/debian trixie non-free" > /etc/apt/sources.list.d/non-free.list; \
    apt-get update; \
    apt-get -y --no-install-recommends install \
        ca-certificates \
        iproute2 \
        iptables \
        dnsmasq \
        procps \
        samba-common-bin \
        ffmpeg \
        tesseract-ocr \
        tesseract-ocr-eng \
        e2fsprogs \
        xorriso \
        genisoimage \
        mtools \
        p7zip-full \
        aria2 \
        zstd \
        wimtools \
    ; \
    # THE PINNED qemu, FROM A DATED SNAPSHOT RATHER THAN FROM trixie.
    echo "deb [check-valid-until=no] https://snapshot.debian.org/archive/debian/${DEBIAN_SNAPSHOT}/ sid main" \
        > /etc/apt/sources.list.d/qemu-snapshot.list; \
    apt-get update; \
    apt-get -y --no-install-recommends -t sid install \
        "qemu-system-x86=${VERSION_QEMU}" \
        "qemu-utils=${VERSION_QEMU}" \
        ovmf \
        # swtpm IS FROM THE SAME SNAPSHOT AS QEMU, so a TPM protocol version and a qemu that speaks
        # it are the same generation. It was MISSING from this list when this file was rewritten
        # from qemux's Dockerfile, and the build's own assertion caught it:
        #
        #     #10 0.114 + swtpm --version
        #     #10 0.114 /bin/sh: 1: swtpm: not found
        #     ERROR: ... did not complete successfully: exit code: 127
        #
        # which is the whole argument for asserting each tool by name at build time rather than
        # finding out when a Windows guest refuses to start.
        swtpm \
    ; \
    rm -f /etc/apt/sources.list.d/qemu-snapshot.list /etc/apt/sources.list.d/non-free.list; \
    apt-get clean; \
    rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/*

# THE BINARY COMES FROM THE BUILD JOB, NOT FROM A GO TOOLCHAIN IN THIS IMAGE.
#
# `dist/easyvm` is built by the workflow's first job with CGO_ENABLED=0 and downloaded here. A
# builder stage in this file would work and would carry the module cache into the build for one
# static binary; a separate job also means the binary has already passed `go test` before it is
# anywhere near an image.
# THE MODE IS SET HERE AS WELL AS IN THE WORKFLOW, and the duplication is deliberate: a COPY keeps
# the source mode, and the artifact it comes from is a zip that carries none - so the workflow
# chmods before the COPY and this line guarantees the result either way. A build that depends on one
# of the two is a build that breaks when the other changes.
COPY --chmod=0755 dist/easyvm /usr/local/bin/easyvm

# A BUILD-TIME SELF-CHECK, AND EVERY LINE IS A TOOL THE GO CODE CALLS BY NAME.
#
# THE CPU ASSERTION IS THE MOST IMPORTANT LINE IN THIS FILE. `-cpu Skylake-Client-v4` is what every
# guest starts with; `-cpu host` makes a run unreproducible across runners and cachyos panicked in
# its first second because of it. A qemu that cannot name the model fails the BUILD here rather than
# every walkthrough's first boot.
RUN set -eux; \
    qemu-system-x86_64 --version | head -1; \
    qemu-img --version | head -1; \
    qemu-system-x86_64 -cpu help 2>&1 | grep -q 'Skylake-Client-v4' || \
      { echo "=== THIS qemu DOES NOT KNOW Skylake-Client-v4 ==="; \
        qemu-system-x86_64 -cpu help 2>&1 | grep -i skylake || true; exit 1; }; \
    ls -l /usr/share/OVMF/OVMF_CODE_4M.fd /usr/share/OVMF/OVMF_VARS_4M.fd; \
    swtpm --version; \
    test -x /usr/sbin/smbd && /usr/sbin/smbd --version | head -1; \
    dnsmasq --version | head -1; \
    ip -V; \
    iptables --version; \
    command -v sysctl ffmpeg tesseract debugfs xorriso genisoimage mformat 7z aria2c unzstd wimlib-imagex; \
    /usr/local/bin/easyvm versions

# THE CONTAINER MUST BE STARTED WITH THE TUN DEVICE AND NET_ADMIN, AND THE PROGRAM REFUSES AT
# STARTUP WITHOUT THEM.
#
# Preflight in internal/net separates the three failures because they have three different fixes:
# /dev/net/tun absent, /dev/net/tun unopenable, and "a bridge cannot be created". The raw failure
# they produce otherwise is `RTNETLINK answers: Operation not permitted`, which reads as a broken
# network rather than a missing capability - and the Python implementation found it at `ip link add`,
# after a guest had been built.
#
# So the run command is:
#
#   docker run --device /dev/net/tun --cap-add NET_ADMIN -p 6080:6080 \
#              -v /tmp/work:/work ghcr.io/.../easyvm serve --work /work
#
# and there is no fallback: qemu's user-mode networking is not available in this program, because
# the host's smbd listens on the bridge address and a user-mode guest cannot route to it.
RUN set -eux; mkdir -p /work
VOLUME ["/work"]

# NO ENTRYPOINT. The qemux base ships one that configures a VM from environment variables; this
# image has no such thing, so the caller names the command. `easyvm serve` for a daemon, `easyvm
# run` is the client and needs no container of its own.
ENTRYPOINT []
WORKDIR /work
