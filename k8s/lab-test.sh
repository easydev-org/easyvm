#!/bin/sh
# RUN ONE .walk LEG BY HAND, ON A POD THAT HAS /dev/kvm.
#
#   k8s/lab-test.sh fedora-workstation ssh /tmp/w/linux/fedora-workstation/steps/ssh.walk
#
# WHY THIS EXISTS SEPARATELY FROM lab-run.sh: that script resolves `/walks/<os>-<leg>.yaml` by a
# naming convention the repository no longer uses - the files are `.walk` now, and a leg under test
# is usually a WORKING COPY at /tmp/w, not the mounted /walks. This takes the walkthrough path as an
# argument, so a file can be tested before it is committed.
#
# IT DOES NOT START A DAEMON. The pod runs `easyvm serve --work /work` as PID 1 (k8s/lab.yaml), and
# one daemon runs every leg: `stop` ends a run, `destroy` clears a sandbox, and the server stays.
# Starting a second one is what put two of them in one pod and failed the next boot on a busy port.
#
# THE DISK IS TAKEN FROM /disks/pulled AND COPIED IN, because the daemon always boots /work/disk.qcow2.
set -eu

os=${1:?os, e.g. fedora-workstation}
leg=${2:?leg, e.g. ssh}
walk=${3:?path to the .walk file}

src=""
for f in "/disks/pulled/$os.qcow2" "/disks/pulled/distro.qcow2" "/disks/os/$os/install.qcow2" "/disks/os/$os/setup.qcow2"; do
    [ -f "$f" ] && { src="$f"; break; }
done
[ -n "$src" ] || { echo "=== no disk for $os; looked in /disks/pulled and /disks/os/$os" >&2; exit 1; }
echo "=== parent disk: $src"

# THE BINARY IS A PARAMETER, because the pod's image may predate the `.walk` parser and running the
# leg through it would test the old code - measured: `yaml: line 4: mapping values are not allowed`,
# which is the OLD loader refusing a file the new one reads.
EV=${EV:-easyvm}
echo "=== client: $EV ($($EV check "$walk" 2>&1 | head -1))"

echo "=== stop anything in progress"
$EV stop --work /work >/dev/null 2>&1 || true
sleep 3
$EV destroy --work /work >/dev/null 2>&1 || true
sleep 2
ip link del qemutap0 2>/dev/null || true

# THE DAEMON MUST BE THE SAME BUILD AS THE CLIENT, or the client sends a request the old parser
# reads with its old vocabulary. The pod's PID 1 is `easyvm serve` from the image, so it is replaced
# by a serve from the binary under test, on the same work directory and socket.
echo "=== restart the daemon on $EV"
for p in $(ps -eo pid,comm 2>/dev/null | awk '$2 ~ /^easyvm$/ { print $1 }'); do
    [ "$p" = "1" ] && kill -TERM 1 2>/dev/null || kill -TERM "$p" 2>/dev/null || true
done
sleep 4
nohup $EV serve --work /work --http :8080 > /tmp/daemon-under-test.log 2>&1 < /dev/null &
sleep 6
$EV status --work /work 2>&1 | head -3 || true

echo "=== copy the parent in"
rm -f /work/disk.qcow2
cp "$src" /work/disk.qcow2

echo "=== run $walk"
exec $EV run "$walk" --work /work --boot-disk /work/disk.qcow2
