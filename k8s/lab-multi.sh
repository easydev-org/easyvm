#!/bin/sh
# RUN SEVERAL OPERATING SYSTEMS AT ONCE, ONE POD EACH.
#
#   k8s/lab-multi.sh windows:install macos:setup ghostbsd:gbi ubuntu:install
#
# WHY ONE POD PER OS RATHER THAN ONE POD RUNNING FOUR
#
# A POD IS THE RESET, and it is also the ISOLATION. Four machines in one pod would share one tap name,
# one 5900 port, one QMP socket path and one work directory - every one of those is a singleton in
# `internal/net` and `internal/qmp`, correctly, because a guest is a machine and a machine has one of
# each. Separate pods give each guest its own; the PVCs are what they share, which is where the
# artifacts and the disks belong.
#
# THE NODE HAS 128 CPUs, 2 TiB of memory and 30 `squat.ai/kvm` permits, and each pod asks for 6 CPUs,
# 12 GiB and one permit, so four at once is a small fraction of what is there.
#
# /disks IS SHARED THROUGH A PVC, WHICH IS ReadWriteOnce - so every pod must land on the same node.
# It does: there is one node.
set -eu

here=$(dirname "$0")
[ $# -gt 0 ] || { echo "usage: lab-multi.sh os:leg [os:leg ...]" >&2; exit 2; }

# THE POD NAME IS THE OS, so `kubectl logs` and `kubectl exec` do not need a lookup table.
for pair in "$@"; do
    os=${pair%%:*}
    pod="easyvm-$os"

    echo "=== $pod"
    kubectl delete pod "$pod" -n develop --wait=true --ignore-not-found 2>/dev/null || true

    sed -e "s/^  name: easyvm-lab$/  name: $pod/" \
        -e "s/^    app: easyvm-lab$/    app: $pod/" \
        "$here/lab.yaml" > "/tmp/$pod.yaml"

    kubectl apply -f "/tmp/$pod.yaml" >/dev/null
done

# ONE WAIT FOR ALL OF THEM, so four starts do not cost four times the latency.
i=0
while [ $i -lt 90 ]; do
    pending=0
    for pair in "$@"; do
        os=${pair%%:*}
        phase=$(kubectl get pod "easyvm-$os" -n develop -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
        case "$phase" in
            Running) ;;
            Failed|Succeeded) echo "easyvm-$os is $phase" >&2; return 1 2>/dev/null || exit 1 ;;
            *) pending=$((pending + 1)) ;;
        esac
    done
    [ "$pending" = "0" ] && break
    i=$((i + 1)); sleep 2
done

for pair in "$@"; do
    os=${pair%%:*}
    leg=${pair##*:}
    pod="easyvm-$os"
    echo "=== starting $os/$leg in $pod"
    kubectl exec "$pod" -n develop -- sh -c \
        "nohup sh /tools/lab-run.sh '$os' '$leg' > /disks/os/$os-$leg.log 2>&1 &" 2>&1 | head -2
done

echo
echo "=== follow one with:"
for pair in "$@"; do
    os=${pair%%:*}
    echo "  kubectl exec -n develop easyvm-$os -- tail -f /disks/os/$os-${pair##*:}.log"
done
