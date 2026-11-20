#!/usr/bin/env python3
"""Measure a `mode: like` reference before it goes into a walkthrough.

WHY THIS EXISTS. Every `reference:` in this repository is a claim of the form
"this rectangle on that frame means this state". The claim is only worth
anything if it has been MEASURED against frames that should NOT match - and
until now that measurement was done by hand, once per reference, in a throwaway
shell snippet. This is that snippet, made repeatable, because the failure it
prevents is quiet: a reference that matches everything (a white card, a black
screen) makes its check pass on pages it was never about, and the walkthrough
then acts on the wrong screen with no error anywhere.

THE ENGINE'S RULE, REPRODUCED HERE. `internal/run/ocr.go` compares a cropped
region against the same crop of the reference and counts a pixel as unchanged
when EVERY channel is within 8 levels. The score is 1 - changed/total. This tool
uses exactly that rule, so a number measured here is the number the run reports.

USAGE

    # 1. What does this rect score on each of these frames?
    tools/reflike.py score --reference steps/reference/page.png \
        --rect 710,530,1215,655 \
        --frame match://tmp/run/about-you.png \
        --frame reject://tmp/run/privacy.png \
        --frame reject://tmp/run/enterprise.png

    # 2. Suggest a rect that separates the match from every reject, and print
    #    the range to write in the file.
    tools/reflike.py suggest --reference steps/reference/page.png \
        --match /tmp/run/about-you.png \
        --reject /tmp/run/privacy.png --reject /tmp/run/enterprise.png

    # 3. Scan the frames a recording contains, at a time step, and report which
    #    ones are close to the reference - useful for finding the frame that
    #    SHOULD have matched.
    tools/reflike.py scan --reference steps/reference/page.png \
        --rect 710,530,1215,655 --video /tmp/run/ssh.mp4 \
        --every 20

THE `match://` AND `reject://` PREFIXES are only labels for the report; a bare
path is treated as a reject (the safe assumption - a frame you are unsure about
should not be allowed to widen the range).

EXIT STATUS is 1 when the measured range would also accept a reject, so this can
be used as a check in a script.
"""

import argparse
import subprocess
import sys
import tempfile
from pathlib import Path

try:
    from PIL import Image
except ImportError:  # pragma: no cover - the message is the point
    sys.exit("reflike: Pillow is required (pip install pillow)")

import numpy as np

# THE ENGINE'S OWN THRESHOLD, and it must not drift from ocr.go's `screen.Compare(..., 8)`.
TOLERANCE = 8

# The gap left on both sides of a suggested range, so ordinary rendering jitter
# (antialiasing, a cursor blink) does not flip the answer.
MARGIN = 0.02


def load(path):
    try:
        return Image.open(path).convert("RGB")
    except Exception as exc:
        sys.exit(f"reflike: cannot read {path}: {exc}")


def score(reference, frame_path, rect):
    """1 - changed/total over the rect, by the engine's rule."""
    frame = load(frame_path)
    if frame.size != reference.size:
        sys.exit(
            f"reflike: {frame_path} is {frame.size}, the reference is {reference.size}. "
            "The engine crops BOTH with the same rect, so they have to be the same size - "
            "a recording at another resolution needs its own reference."
        )
    a = np.asarray(reference.crop(rect), dtype=np.int16)
    b = np.asarray(frame.crop(rect), dtype=np.int16)
    if a.shape != b.shape:
        sys.exit(f"reflike: rect {rect} is outside one of the images")
    unchanged = (np.abs(a - b).max(axis=2) <= TOLERANCE).mean()
    return float(unchanged)


def parse_rect(text):
    try:
        x0, y0, x1, y1 = (int(v) for v in text.split(","))
    except ValueError:
        sys.exit(f"reflike: --rect wants x0,y0,x1,y1; got {text!r}")
    if not (0 <= x0 < x1 and 0 <= y0 < y1):
        sys.exit(f"reflike: --rect {text!r} is not a positive box")
    return (x0, y0, x1, y1)


def labelled(spec):
    """`match://path` / `reject://path` / plain path -> (kind, path)."""
    if spec.startswith("match://"):
        return "match", spec[len("match://"):]
    if spec.startswith("reject://"):
        return "reject", spec[len("reject://"):]
    return "reject", spec


def cmd_score(args):
    reference = load(args.reference)
    rect = parse_rect(args.rect)
    worst_reject = 0.0
    ok = True
    print(f"reference {args.reference}   rect {rect}   tolerance {TOLERANCE}")
    for spec in args.frame:
        kind, path = labelled(spec)
        s = score(reference, path, rect)
        verdict = ""
        if kind == "match" and s < 0.95:
            verdict = "  <-- a MATCH scoring below 0.95"
            ok = False
        if kind == "reject":
            worst_reject = max(worst_reject, s)
            if s >= 0.95:
                verdict = "  <-- a REJECT scoring at or above 0.95"
                ok = False
        print(f"  {s:.4f}  {kind:6s} {path}{verdict}")
    print(f"\nworst reject = {worst_reject:.4f}")
    return 0 if ok else 1


def cmd_suggest(args):
    reference = load(args.reference)
    match_scores = []
    reject_scores = []

    # COARSE-TO-FINE IS NOT WORTH IT HERE. The rectangles that matter are the ones
    # a person would draw - a field, a button row, a panel - so the candidates are
    # a grid of boxes over the frame's middle, from small to large.
    w, h = reference.size
    candidates = []
    for x0 in range(0, w - 200, 100):
        for y0 in range(0, h - 100, 100):
            for bw in (400, 500, 700, 1000):
                for bh in (120, 200, 300):
                    x1, y1 = min(w, x0 + bw), min(h, y0 + bh)
                    if x1 - x0 < 200 or y1 - y0 < 100:
                        continue
                    candidates.append((x0, y0, x1, y1))

    best = None
    for rect in candidates:
        m = min(score(reference, p, rect) for p in args.match)
        if m < 0.97:
            continue
        r = max(score(reference, p, rect) for p in args.reject)
        gap = m - r
        area = (rect[2] - rect[0]) * (rect[3] - rect[1])
        # PREFER THE BIGGER GAP, THEN THE SMALLER BOX: a small box that separates
        # is more likely to be the actual feature than a large one that happens to
        # include it.
        if best is None or (gap, -area) > (best[0], -best[2]):
            best = (gap, rect, area, m, r)

    if best is None:
        print("no rect separates the match from every reject at >= 0.97. "
              "Either the match frame is not the state you meant, or the reference "
              "is the wrong frame.", file=sys.stderr)
        return 1

    gap, rect, _area, m, r = best
    lo = max(0.0, min(0.95, r + MARGIN))
    hi = 1.0
    print(f"best rect   {list(rect)}")
    print(f"  match   {m:.4f}")
    print(f"  reject  {r:.4f}  (worst)")
    print(f"  gap     {gap:.4f}")
    print(f"\nwrite:  rect: [{rect[0]}, {rect[1]}, {rect[2]}, {rect[3]}], "
          f"range: [{lo:.2f}, {hi:.2f}]")
    return 0


def cmd_scan(args):
    reference = load(args.reference)
    rect = parse_rect(args.rect)
    with tempfile.TemporaryDirectory() as tmp:
        pattern = str(Path(tmp) / "f%05d.png")
        # ONE FRAME EVERY `every` SECONDS, at full resolution: the engine crops at
        # the recording's own size.
        cmd = [
            "ffmpeg", "-loglevel", "error", "-i", args.video,
            "-vf", f"fps=1/{args.every}", pattern,
        ]
        subprocess.run(cmd, check=True)
        frames = sorted(Path(tmp).glob("f*.png"))
        if not frames:
            sys.exit("reflike: ffmpeg produced no frames")
        print(f"{len(frames)} frame(s), one every {args.every}s, rect {rect}")
        best = []
        for i, f in enumerate(frames):
            s = score(reference, str(f), rect)
            best.append((s, i * args.every))
        best.sort(reverse=True)
        for s, t in best[: args.top]:
            print(f"  {s:.4f}  at {t}s")
        if best[0][0] < 0.95:
            print("\nNone of these would match. The reference is from a state this "
                  "recording never showed.", file=sys.stderr)
            return 1
    return 0


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    sub = ap.add_subparsers(dest="cmd", required=True)

    p = sub.add_parser("score", help="score a rect on named frames")
    p.add_argument("--reference", required=True)
    p.add_argument("--rect", required=True)
    p.add_argument("--frame", action="append", required=True,
                   help="match://PATH or reject://PATH, or a bare path (a reject)")
    p.set_defaults(func=cmd_score)

    p = sub.add_parser("suggest", help="find a rect that separates match from reject")
    p.add_argument("--reference", required=True)
    p.add_argument("--match", action="append", required=True)
    p.add_argument("--reject", action="append", required=True)
    p.set_defaults(func=cmd_suggest)

    p = sub.add_parser("scan", help="find the frames of a recording closest to the reference")
    p.add_argument("--reference", required=True)
    p.add_argument("--rect", required=True)
    p.add_argument("--video", required=True)
    p.add_argument("--every", type=int, default=20)
    p.add_argument("--top", type=int, default=8)
    p.set_defaults(func=cmd_scan)

    args = ap.parse_args()
    sys.exit(args.func(args))


if __name__ == "__main__":
    main()
