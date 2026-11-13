#!/bin/sh
# RESET ONE POD TO A KNOWN STATE AND OPTIONALLY START A LEG.
#
#   k8s/reset.sh <os> [leg]
#   k8s/reset.sh ghostbsd install
#
# WHY A FULL POD RECREATE RATHER THAN A CLEANUP
#
# A FAILED RUN KEEPS ITS MACHINE ON PURPOSE - the screen at the failed step is the evidence - so a
# rerun inherits a qemu holding port 5900, an old tap, a stale QMP socket and a daemon that may be
# half-shut. Every one of those produces a message that points somewhere else:
#
#	qemu-system-x86_64: -vnc 127.0.0.1:0: Failed to find an available port: Address already in use
#	boot: qmp .../drive.sock.qmp: connect: connection refused
#
# AND THE POD IS THE QUARANTINE. It carries one guest, its /tmp, its tap names and its ports; deleting
# it removes all of them at once, which is a stronger reset than any sequence of `pkill` and `rm` can
# promise. THE ONLY THING THAT MUST SURVIVE IS /disks, and that is a PVC.
set -eu

os=${1:?os}
leg=${2:-}

here=$(dirname "$0")
pod="easyvm-$os"

echo "=== recreate $pod"
kubectl delete pod "$pod" -n develop --grace-period=0 --force 2>/dev/null || true
# THE FORCED DELETE RETURNS BEFORE THE KUBELET HAS LET GO OF THE PORTS ON THE NODE, so the wait is on
# the pod being ABSENT rather than on the API accepting the delete.
i=0
while [ $i -lt 60 ]; do
    kubectl get pod "$pod" -n develop >/dev/null 2>&1 || break
    i=$((i + 1)); sleep 1
done

sed -e "s/^  name: easyvm-lab$/  name: $pod/" \
    -e "s/^    app: easyvm-lab$/    app: $pod/" \
    "$here/lab.yaml" > "/tmp/$pod.yaml"
kubectl apply -f "/tmp/$pod.yaml" >/dev/null

i=0
while [ $i -lt 180 ]; do
    phase=$(kubectl get pod "$pod" -n develop -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
    [ "$phase" = "Running" ] && break
    i=$((i + 1)); sleep 1
done
[ "$(kubectl get pod "$pod" -n develop -o jsonpath='{.status.phase}')" = "Running" ] || {
    echo "=== $pod never reached Running" >&2; exit 1; }

# THE TWO THINGS THE IMAGE DOES NOT CARRY YET.
#
# `novnc` is in the Containerfile but this pod may predate the image build, and the browser console is
# the whole point of the local loop. The binary is the one just built, so a code change is one command
# away from being tested rather than a rebuild away.
echo "=== install the binary and the console"
kubectl cp /tmp/easyvm-new "develop/$pod:/usr/local/bin/easyvm"
kubectl cp /tmp/novnc.deb "develop/$pod:/tmp/novnc.deb"
kubectl exec "$pod" -n develop -- sh -c '
    cd /tmp && rm -rf nx && dpkg -x novnc.deb nx &&
    mkdir -p /usr/share/novnc && cp -a nx/usr/share/novnc/. /usr/share/novnc/
' >/dev/null

[ -n "$leg" ] || { echo "=== $pod is reset"; exit 0; }

echo "=== $os:$leg"
kubectl exec "$pod" -n develop -- sh -c "nohup sh /tools/lab-run.sh $os $leg > /disks/os/$os-$leg.log 2>&1 &"
sleep 3
echo "=== started; follow with:"
echo "  kubectl exec $pod -n develop -- easyvm status --work /disks/os/$os/run-$leg"
