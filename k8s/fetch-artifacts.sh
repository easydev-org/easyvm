#!/bin/sh
# FILL THE ARTIFACT PVC FROM ghcr, RESUMING ACROSS RESETS.
#
#   k8s/fetch-artifacts.sh            every ISO the four priority OSes need
#   k8s/fetch-artifacts.sh iso-windows11
#
# THE LAYOUT IS THE POINT: /artifacts/isos/<release>.iso, because `fetch:` inside a walkthrough looks
# for `<release>.iso` or `installer.iso` in the WORK DIRECTORY FIRST and only reaches a registry when
# the file is absent. A pre-seeded PVC therefore means the local loop never pulls - and the YAML stays
# byte-identical to CI's, with no tag swapped and no registry overridden.
#
# RESUME, NOT RETRY. `.github/pull-artifact.sh` fetches the blob with `Range:`; `oras pull` cannot,
# and a retry around it restarts every multi-gigabyte transfer at zero.
set -eu

repo=${DISKS_REPOSITORY:-easydev-org/easyvm}
dest=${ARTIFACTS:-/artifacts}
mkdir -p "$dest/isos"

# THE REGISTRY TAG AND THE FILENAME ARE DIFFERENT THINGS: `iso-ubuntu-desktop` is published under that
# tag and `fetch: linux_iso release: ubuntu` looks for `ubuntu.iso`, because the alias table maps
# ubuntu -> ubuntu-desktop. Both names are here, so neither side has to guess.
fetch() {
    tag=$1
    name=$2
    if [ -f "$dest/isos/$name" ]; then
        echo "=== $name is already here ($(stat -c %s "$dest/isos/$name") bytes)"
        return 0
    fi
    echo "=== $tag -> isos/$name"
    sh /walks/.github/pull-artifact.sh ghcr.io "$repo" "$tag" "$dest/isos/$name" \
        || echo "  (FAILED: $tag)"
}

# THE FOUR PRIORITY OSES FIRST, because these are the ones being worked on.
fetch iso-ubuntu-desktop   ubuntu.iso
fetch iso-windows11        windows11.iso
fetch iso-virtio-win       virtio-win.iso
fetch iso-ghostbsd-mate    ghostbsd-mate.iso

# AND THE REST, so a later dispatch of any other OS does not have to fetch again.
for pair in \
    iso-kubuntu-desktop:kubuntu-desktop.iso \
    iso-fedora-workstation:fedora-workstation.iso \
    iso-mint-cinnamon:mint-cinnamon.iso \
    iso-opensuse-leap-16.1:opensuse-leap-16.1.iso \
    iso-cachyos-desktop:cachyos-desktop.iso \
    iso-manjaro-kde:manjaro-kde.iso \
    iso-endeavouros:endeavouros.iso \
    iso-zorin-core:zorin-core.iso \
    iso-mx-xfce:mx-xfce.iso \
    iso-popos-desktop:popos-desktop.iso \
    iso-nixos-graphical:nixos-graphical.iso
do
    fetch "${pair%%:*}" "${pair##*:}"
done

echo "=== /artifacts/isos"
ls -la "$dest/isos" 2>/dev/null | awk 'NR>3 {printf "  %-30s %6d MiB\n", $9, $5/1048576}'
