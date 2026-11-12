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
mkdir -p "$WORK" "$SHOT"

echo "=== daemon"
easyvm serve --work "$WORK" --novnc-port 0 > "$WORK/daemon.log" 2>&1 &
DPID=$!
i=0
while [ $i -lt 30 ]; do
  [ -S "$WORK/easyvm.sock" ] && break
  i=$((i+1)); sleep 1
done

echo "=== run $FILE"
easyvm run "$FILE" --work "$WORK" > "$WORK/run.log" 2>&1 &
RPID=$!

# WHILE IT RUNS: copy every new screendump out as a PNG, and print the step the daemon is on.
#
# THE FRAMES ARE NUMBERED BY THE DAEMON, so "which frame is which step" is answered by the log rather
# than by a timestamp - `[HH:MM:SS] running step 7/27` lines up with `frame-0007` because a `record:`
# step's `poll:` decides the cadence and the walk advances at its own pace.
seen=0
while kill -0 "$RPID" 2>/dev/null; do
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
    "$(easyvm status --work "$WORK" 2>/dev/null | grep -oE 'step [0-9]+/[0-9]+ "[^"]*"' | head -1)"
  sleep 5
done

wait "$RPID" 2>/dev/null || true
echo "=== the run ended"
tail -8 "$WORK/run.log" 2>/dev/null || true
echo "=== $seen frame(s) copied to $SHOT as PNG"
ls "$SHOT" | tail -12
easyvm stop --work "$WORK" 2>/dev/null || true
kill "$DPID" 2>/dev/null || true
