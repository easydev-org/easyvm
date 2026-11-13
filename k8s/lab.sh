#!/bin/sh
# REBUILD THE LAB POD AND RUN ONE LEG IN IT - ONE POD PER RUN, NO STATE CARRIED OVER.
#
#   k8s/lab.sh ubuntu install
#   k8s/lab.sh ubuntu ssh
#
# WHY THE POD IS RECREATED RATHER THAN REUSED
#
# A POD IS THE RESET. Running twice inside one pod leaves a qemu, a tap and a half-written overlay
# behind, and the second run then fails on a symptom of the first - which is exactly what happened
# while this script did not exist: `pkill -9 easyvm` left a defunct daemon and a stale `qemutap0`, and
# the next boot reported a tap it could not create. `internal/net` cleans the tap on every boot, but a
# killed daemon also leaves the QMP and VNC ports held, and there is no reason to reason about which
# leftovers are harmless when deleting the pod is one command.
#
# THE STATE THAT MUST SURVIVE IS ON THE PVCs, NOT IN THE POD:
#
#	/disks/os/<os>/install.qcow2    what a leg wrote, so the next leg can continue from it
#	/artifacts/isos/*.iso           the installer images, fetched once from ghcr
#
# so recreating the pod costs a few seconds and loses nothing.
set -eu

os=${1:?os}
leg=${2:-install}

echo "=== recreating the pod"
kubectl delete pod easyvm-lab -n develop --wait=true --ignore-not-found 2>/dev/null || true
kubectl apply -f "$(dirname "$0")/lab.yaml" >/dev/null
i=0
while [ $i -lt 60 ]; do
    phase=$(kubectl get pod easyvm-lab -n develop -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
    [ "$phase" = "Running" ] && break
    i=$((i + 1)); sleep 2
done
[ "$phase" = "Running" ] || { echo "the pod never reached Running (phase $phase)" >&2; exit 1; }

echo "=== running $os/$leg"
exec kubectl exec easyvm-lab -n develop -- sh /tools/lab-run.sh "$os" "$leg"
