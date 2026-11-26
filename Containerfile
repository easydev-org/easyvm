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
FROM debian:trixie-slim AS easyvm-runtime

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
#   samba             `smbd`, AND IT IS NOT IN `samba-common-bin`. That package carries the shared
#                     libraries, `smbclient`, `smbpasswd` and the init scripts; the DAEMON itself is
#                     in `samba`. Measured: a container built with only `samba-common-bin` answered
#
#                         serve the host share: smbd is not installed: exec: "smbd":
#                         executable file not found in $PATH
#
#                     - a sentence that names the binary and not the package, which is why the
#                     comment here said the wrong one for as long as the code did not run.
#
#                     THE FULL PACKAGE IS TAKEN AND NOTHING ELSE IS STARTED FROM IT: winbind and the
#                     AD tooling are for a domain member and this is a standalone share, so they are
#                     installed and never run. Splitting the package would mean a metapackage of one
#                     binary, and the cost of the working one is 30 MB in an image that already
#                     carries qemu.
#
#                     The server is host-side on purpose - `-netdev user,smb=` makes qemu fork its
#                     own smbd, which is a second server with a second config, and telling the two
#                     apart cost several runs.
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
#   mtools            mformat/mcopy/mdir/mmd: build a FAT image without loop-mounting, which a
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
# CURL IS HERE BECAUSE `prepare` FETCHES ORAS WITH IT, AND THIS IMAGE HAD NO CURL AT ALL. Measured:
#
#	/__w/_temp/....sh: 53: curl: not found
#	Process completed with exit code 127
#
# It is 250 KiB and it is the one tool every fetch in the workflows assumes. aria2 was the
# alternative - it is already here for the macOS recovery image - but its flags differ between
# versions enough that writing one blind is how a download silently produces nothing.
#
# THE NOTE IS OUT HERE AND NOT INSIDE THE `RUN` FOR A REASON THAT COST A DEBUGGING ROUND: inside a
# backslash-continued command a `#` line is not a shell comment, it is part of the argument list.
# Verified:
#
#	apt-get install \
#	    # a comment \
#	    curl \
#	    iproute2
#	+ curl iproute2            <- apt never ran; the comment ate the continuation
#
# tcpdump IS THE ONE TOOL THE SSH DIAGNOSIS WAS MISSING. When host->guest is a timeout but ICMP
# reaches the guest, the only question left is whether the SYN ever arrives on the tap (the host
# dropped it) or arrives and the guest never answers (guest TCP). Nothing in this image could show
# that, so the two cases were indistinguishable and several engine "fixes" were guesses. 8 MB.
RUN set -eux; \
    # non-free is where some qemu firmware blobs live; the snapshot below is official Debian.
    echo "deb https://deb.debian.org/debian trixie non-free" > /etc/apt/sources.list.d/non-free.list; \
    apt-get update; \
    apt-get -y --no-install-recommends install \
        ca-certificates \
        curl \
        iproute2 \
        iptables \
        tcpdump \
        dnsmasq \
        procps \
        samba \
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
        # novnc IS WHAT MAKES THE MACHINE VISIBLE IN A BROWSER, and its absence is silent.
        #
        # `internal/daemon/console.go` serves /usr/share/novnc verbatim and bridges its WebSocket to
        # qemu's VNC port - and when the directory is missing it logs one line and returns, because
        # "the guest is the product, the page is how a person looks at it" and taking a VM down over
        # a console script is the wrong trade. The effect was that every local pod reported a noVNC
        # URL that nothing listened on:
        #
        #	noVNC:   http://127.0.0.1:6080/vnc.html      <- printed by the launcher, unconditionally
        #	noVNC: no page at /usr/share/novnc/vnc.html; the guest is reachable on VNC port 5900 only
        #
        # WHICH ARE THE TWO HALVES OF THE SAME CONFUSION, and both are fixed: the package is here, and
        # the launcher now prints the URL only when the daemon says a page exists.
        novnc \
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
    command -v sysctl ffmpeg tesseract debugfs xorriso genisoimage mformat mcopy mdir mmd 7z aria2c unzstd wimlib-imagex; \
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

# ==============================================================================
# THE TEST IMAGE: THE SAME MACHINE, WITH A GO TOOLCHAIN.
#
# `go test ./...` USED TO RUN ON THE RUNNER, AND THE RUNNER IS NOT THE MACHINE A WALKTHROUGH USES.
# That difference is not academic: on 2026-11-16 a test that needs `qemu-img` failed the `binary` job
# with
#
#	qemu-img create: exec: "qemu-img": executable file not found in $PATH
#
# and the tempting fix - skip the test when the tool is missing - is the wrong one. A test that cannot
# run is not a test, and `qemu-img` is in THIS image for the reason this image exists, so the honest
# answer is to run the tests WHERE THE TOOL IS.
#
# WHAT THIS STAGE HAS THAT A BARE RUNNER DOES NOT, AND WHY EACH IS NEEDED:
#
#	qemu-img, qemu-system-x86_64   spawned by name in the package under test
#	tesseract                      the OCR steps read real screens
#	smbd, swtpm, OVMF              `share_serve:`, the Windows install, every UEFI guest
#	/dev/net/tun, /dev/kvm         at RUN time only; `behaviour` asks for them explicitly
#
# IT IS NOT THE SHIPPED IMAGE, and the stage below is written after this one so that `docker build .`
# with no `--target` still means the product. See the note there.
FROM easyvm-runtime AS tester

ARG DEBIAN_FRONTEND=noninteractive
# THE PIN IS `go.mod`'S, NOT WHATEVER THE DISTRO PACKAGES. A toolchain older than a directive the
# code uses fails as a compile error in a file nobody changed.
ARG GO_VERSION="1.26.0"

RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates curl; \
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -o /tmp/go.tgz; \
    tar -C /usr/local -xzf /tmp/go.tgz; \
    rm -f /tmp/go.tgz; \
    /usr/local/go/bin/go version; \
    rm -rf /var/lib/apt/lists/*

ENV PATH="/usr/local/go/bin:${PATH}"
ENV GOCACHE=/tmp/go-cache
ENV GOFLAGS=-buildvcs=false

# THE SOURCE IS BIND-MOUNTED AT RUN TIME, not copied here: a change to one Go file must not
# invalidate the qemu layer above it.
WORKDIR /src
ENTRYPOINT ["/usr/local/go/bin/go"]
CMD ["test", "./...", "-count=1"]


# ==============================================================================
# THE SHIPPED IMAGE, AND IT IS LAST ON PURPOSE.
#
# DOCKER'S DEFAULT TARGET IS THE LAST STAGE IN THE FILE, and `docker/build-push-action` uses that
# default when a workflow names no `target:` - which is exactly what `image.yml` does. Writing this
# stage after `tester` is what keeps `docker build .` meaning the product.
#
# MEASURED: with `tester` last, a `docker build .` published the TEST IMAGE as
# `ghcr.io/easydev-org/easyvm:latest` - a container whose entrypoint is `go test`. Every walkthrough
# would have started a test run instead of a machine, and the failure would have surfaced as
# `serve: executable file not found` in whichever workflow ran first.
FROM easyvm-runtime AS runtime

# THE ENTRYPOINT IS THE PROGRAM, BECAUSE THAT IS THE ONLY THING THIS IMAGE IS FOR.
#
# `ENTRYPOINT []` WAS WRONG AND IT COST A RUN (43014353957). An empty entrypoint makes docker treat
# the first word of the command as an EXECUTABLE, so
#
#     docker run ghcr.io/easydev-org/easyvm:latest serve --work /work
#
# fails with `exec: "serve": executable file not found in $PATH` and exit 127 - while
#
#     docker run ghcr.io/easydev-org/easyvm:latest versions
#
# fails the same way, and only an explicit `--entrypoint /usr/local/bin/easyvm` works. That is not
# a usable image: every caller would have to know a path inside it.
#
# CMD IS STILL THE CALLER'S. An entrypoint and a default command are two different things, and the
# second is left empty on purpose - `serve` is not a default this program should assume, because a
# container that starts a daemon when the caller wanted `check` is worse than one that prints usage.
# `serve` IS THE DEFAULT COMMAND BECAUSE THIS IMAGE EXISTS TO BE A DAEMON.
#
# `CMD []` WAS THE OPPOSITE DECISION, and it pushed the same four lines into every caller: background
# the daemon, wait for its "listening on" line, then talk to it. MEASURED across this repository's own
# workflows, three separate places had written that block and two of them had got the wait wrong - one
# waited on a socket file that exists before the daemon can answer, the other did not wait at all and
# got `dial tcp 127.0.0.1:8080: connect: connection refused`.
#
# SO THE IMAGE IS PID 1 WITH THE DAEMON RUNNING, which is what a container is for: one process, and the
# platform stops it. A caller that wants a different verb still passes one - `docker run --rm <image>
# versions` runs `versions` because the ENTRYPOINT takes argv - and `--entrypoint` remains available
# for a shell.
#
# `--work /work` MATCHES THE WORKDIR BELOW, so the default invocation needs no arguments at all.
ENTRYPOINT ["/usr/local/bin/easyvm"]
CMD ["serve", "--work", "/work", "--http", ":8080"]
WORKDIR /work
