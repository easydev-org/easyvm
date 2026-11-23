#!/usr/bin/env python3
"""Print the byte offset of the EFI System Partition in a qcow2 disk.

    esp-offset.py <disk.qcow2>

WHY THIS IS A FILE RATHER THAN A HEREDOC IN THE WORKFLOW. A `run: |` block is a YAML scalar, so every
line of an embedded script must be indented to the block's column - and a `<<'EOF'` terminator must be
at column zero. The two cannot both be true, and the error a reader gets is a YAML parse failure
pointing at the script's own source.

WHY NOT `sgdisk`. It is not installed in this image. A GPT header is a 92-byte structure at LBA 1
that names the entry array; reading those two things with qemu-io answers the question, and qemu-io
is here because qemu is.
"""
import struct
import subprocess
import sys


def read(disk, offset, length):
    """`length` bytes at `offset`, through qemu-io's hex dump.

    NOT `qemu-io read -s`: `-s` is `--snapshot` and the read is refused. `read -v` prints
    `%08x:  ` then sixteen byte pairs then an ASCII column, which is what is parsed here.
    """
    out = subprocess.run(
        ["qemu-io", "-f", "qcow2", "-r", "-c", "read -v %d %d" % (offset, length), disk],
        capture_output=True, text=True).stdout
    data = bytearray()
    for line in out.splitlines():
        colon = line.find(":")
        if colon < 0:
            continue
        for field in line[colon + 1:].split()[:16]:
            if len(field) != 2:
                break
            data.append(int(field, 16))
    return bytes(data)


def main():
    if len(sys.argv) != 2:
        sys.exit("usage: esp-offset.py <disk.qcow2>")
    disk = sys.argv[1]

    header = read(disk, 512, 92)
    if header[0:8] != b"EFI PART":
        sys.exit("no GPT header at LBA 1: this disk has no partition table to read")

    entry_lba, count, size = struct.unpack("<QII", header[72:88])
    table = read(disk, entry_lba * 512, count * size)

    # C12A7328-F81F-11D2-BA4B-00A0C93EC93B, in GPT's own byte order.
    esp_type = bytes.fromhex("28732ac11ff8d211ba4b00a0c93ec93b")
    for i in range(count):
        entry = table[i * size:(i + 1) * size]
        if entry[0:16] != esp_type:
            continue
        first, _ = struct.unpack("<QQ", entry[32:48])
        print(first * 512)
        return
    sys.exit("no EFI System Partition in this disk")


if __name__ == "__main__":
    main()
