#!/bin/sh
# RUN A WALK BY HAND AND WATCH IT, USING THE SAME PICTURES CI USES.
#
#     sh /work/watch.sh /work/zorin-install.yaml
#
# WHY THIS EXISTS: every fix in this repository has been argued from a RECORDING that is only
# available AFTER a leg ends, so a pane that sits seventy pixels lower costs a thirty-minute round
# trip. Here the walk runs in front of you and the frames are already on disk while it does.
#
# THE PICTURES COME FROM `screendump`, WHICH IS WHAT CI'S `record:` STEP USES. There is no VNC
# decoder in this image (`ffmpeg -formats` has no `vnc` demuxer - measured) and no python3, socat or
# nc to speak QMP by hand. `easyvm` itself already does the only thing needed here: a `record:` step
# writes numbered PPM frames into the work directory and encodes them at the end.
#
# SO THIS STARTS THE DAEMON, RUNS THE FILE, AND CONVERTS THE FRAMES AS THEY ARRIVE. The daemon's own
# `frames/frame-*.ppm` sequence is the source, and `-vf scale` is only there because a PPM of 1920x1080
# is 6 MB and a PNG of it is 300 KB.
set -eu

FILE=${1:-/work/zorin-install.yaml}
WORK=${WORK:-/work/w}
SHOT=${SHOT:-/work/shots}
INTERVAL=${INTERVAL:-5}
mkdir -p "$WORK" "$SHOT"

echo "=== daemon"
easyvm serve --work "$WORK" > "$WORK/daemon.log" 2>&1 &
DPID=$!
i=0
while [ $i -lt 30 ]; do
  [ -S "$WORK/easyvm.sock" ] && break
  i=$((i+1)); sleep 1
done

echo "=== run $FILE"
easyvm run "$FILE" --work "$WORK" > "$WORK/run.log" 2>&1 &
RPID=$!

# `easyvm run` IS A CLIENT AND IT RETURNS AS SOON AS THE REQUEST IS ACCEPTED - measured here: it
# exited in three seconds while the daemon was still booting the ISO, and the loop's `kill -0 $RPID`
# then reported "the run ended" and the cleanup killed a working guest. What has to be watched is the
# DAEMON, whose `status` says `starting` / `running step N` / `failed` / `finished`.
#
# SO THE LOOP FOLLOWS `easyvm status` AND NOT A PID: it ends when the status line says the walk is
# over, or when the daemon itself is gone.
#
# THE FRAMES ARE NUMBERED BY THE DAEMON, so "which frame is which step" is answered by the log rather
# than by a timestamp - `[HH:MM:SS] running step 7/27` lines up with `frame-0007` because a `record:`
# step's `poll:` decides the cadence and the walk advances at its own pace.
seen=0
while :; do
  st=$(easyvm status --work "$WORK" 2>/dev/null || true)
  case "$st" in
    *"status: failed"*|*"status: finished"*) break ;;
  esac
  kill -0 "$DPID" 2>/dev/null || break
  for f in "$WORK"/frames/frame-*.ppm; do
    [ -e "$f" ] || continue
    # THE DAEMON NAMES THEM `frame-%06d.ppm`, so stripping non-digits gives a sortable integer.
    n=$(basename "$f" .ppm | tr -dc '0-9')
    [ -n "$n" ] || continue
    [ "$n" -le "$seen" ] 2>/dev/null && continue
    ffmpeg -hide_banner -loglevel error -nostdin -y -i "$f" \
           -vf "scale=1280:-1" "$SHOT/frame-$n.png" 2>/dev/null || true
    seen=$n
  done
  printf '%s  %s\n' "$(date +%H:%M:%S)" \
    "$(printf '%s' "$st" | grep -oE 'step [0-9]+/[0-9]+ "[^"]*"' | head -1)"
  sleep "$INTERVAL"
done

wait "$RPID" 2>/dev/null || true
echo "=== the run ended"
tail -8 "$WORK/run.log" 2>/dev/null || true
echo "=== $seen frame(s) copied to $SHOT as PNG"
ls "$SHOT" | tail -12
easyvm stop --work "$WORK" 2>/dev/null || true
kill "$DPID" 2>/dev/null || true
