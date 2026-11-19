#!/usr/bin/env python3
"""rectpick: narrow the full-frame `rect:` of every `check:`, measured from a real recording.

WHY THIS IS IN THE TREE RATHER THAN IN /tmp. It was written there, used to narrow 82 checks, and
then a round of cleanup removed it - and the checks it had written stayed. A tool whose output is
committed belongs beside the thing it produces, where the next person can rerun it and see whether
a rect still matches the recording it came from.

THE PROBLEM IT SOLVES. Many `check:` steps read the WHOLE FRAME. A whole frame is where OCR is
weakest (the awaited words are a few hundred pixels in 1920x1080) and where a match can land in a
pane that has nothing to do with the step. The band holding the words is small, and the same band is
where the reading is reliable.

HOW A RECT IS CHOSEN, AND WHAT IS VERIFIED:

  1. SAMPLE THE RECORDING and OCR each sample ONCE with tesseract's TSV output, which carries
     left/top/width/height per word. Results are cached, so a second file over the same video is
     nearly free.

  2. FIND THE WORDS, in every sample, and compute the smallest box holding one occurrence of each
     awaited word. Smallest, not the union of all occurrences: a title bar holds `installer` and a
     dialog body holds `welcome`, and a box spanning both describes neither.

  3. WALK THE CHECKS IN FILE ORDER AND MOVE FORWARD IN TIME. A walk is a sequence. A check later in
     the file happens later in the recording. The search starts at the frame the previous check
     landed on, and falls back to the whole recording because a walk that retries reaches a page
     more than once.

  4. REFUSE THE CASES THE MEASUREMENT ITSELF REPORTS AS UNRELIABLE:
       - a box hugging the top or bottom edge is a title or status bar, and a word there is
         decoration (measured: freebsd's `install` is a status-bar item at y=1038 and a menu row on
         the page the check is about);
       - a box covering most of the frame is not a narrowing;
       - a word that sits in more than one distinct place is not a landmark;
       - a one-word branch is too easy to match by accident, so a frame only counts when at least
         half the branches are present (two-word-and-longer branches need only one).

  5. PROVE IT READS. The box is cropped from the frames that had the words and re-read at scale 1,
     then 2, then 3; the scale with the most successful re-reads is the one written. A box that
     reads in NO frame is not written, and the check is named.

usage:
  tools/rectpick.py <walk-file> <video> [--every SECONDS] [--cache DIR] [--apply] [--jobs N]

WITHOUT --apply it only reports what it would write.
"""
import collections
import concurrent.futures as cf
import json
import os
import re
import subprocess
import sys

W, H = 1920, 1080
PAD = 24


def probe(video):
    return float(subprocess.check_output(
        ["ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", video]
    ).decode().strip())


def extract(args):
    video, t, cache = args
    p = os.path.join(cache, "f%06d.jpg" % int(t))
    if not os.path.exists(p):
        subprocess.run(["ffmpeg", "-loglevel", "error", "-ss", "%.1f" % t, "-i", video,
                        "-frames:v", "1", "-q:v", "2", "-y", p],
                       check=False, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    if os.path.exists(p) and os.path.getsize(p) > 2000:
        return int(t)
    return None


def tsv_words(path):
    """Word boxes as [(text, x, y, w, h)]; empty when tesseract read nothing."""
    try:
        raw = subprocess.check_output(["tesseract", path, "stdout", "tsv"],
                                      stderr=subprocess.DEVNULL).decode("utf-8", "replace")
    except (subprocess.CalledProcessError, FileNotFoundError):
        return []
    out = []
    for line in raw.split("\n")[1:]:
        f = line.split("\t")
        if len(f) < 12 or f[0] != "5":
            continue
        text = f[11].strip()
        if not text:
            continue
        out.append([text, int(f[6]), int(f[7]), int(f[8]), int(f[9])])
    return out


def norm(s):
    return "".join(c for c in s.lower() if c.isalnum())


def parse_sets(text):
    """`[[a, b], [c]]` -> [['a','b'], ['c']]; `[a, b]` -> [['a'], ['b']].

    THE FLAT FORM IS A LIST OF ONE-WORD SETS, not one set of two words: `judgeWords` accepts the
    check when ANY of those sets is satisfied. Keeping them separate is what lets the box cover all
    of them.
    """
    if "words: [[" in text:
        body = text.split("words: ", 1)[1]
        i = body.find("[[")
        depth = 0
        for j in range(i, len(body)):
            if body[j] == "[":
                depth += 1
            elif body[j] == "]":
                depth -= 1
                if depth == 0:
                    break
        return [[w.strip() for w in part.split(",") if w.strip()]
                for part in re.findall(r"\[([^\[\]]*)\]", body[i + 1:j])]
    m = re.search(r"words: \[([^\[\]]*)\]", text)
    if not m:
        return []
    return [[w.strip()] for w in m.group(1).split(",") if w.strip()]


def checks(path):
    out = []
    for no, line in enumerate(open(path).read().split("\n"), 1):
        if "rect: [0, 0, 1920, 1080]" not in line:
            continue
        out.append({"line": no, "sets": parse_sets(line), "text": line})
    return [c for c in out if c["sets"]]


def tightest(occ):
    combos = [()]
    for lst in occ:
        combos = [c + (b,) for c in combos for b in lst[:6]][:300]
    best, bestarea = None, None
    for c in combos:
        x0 = min(b[1] for b in c)
        y0 = min(b[2] for b in c)
        x1 = max(b[1] + b[3] for b in c)
        y1 = max(b[2] + b[4] for b in c)
        area = (x1 - x0) * (y1 - y0)
        if bestarea is None or area < bestarea:
            best, bestarea = (x0, y0, x1, y1), area
    return best


def pad(b):
    return [max(0, b[0] - PAD), max(0, b[1] - PAD), min(W, b[2] + PAD), min(H, b[3] + PAD)]


def crop_words(args):
    frame, rect, scale = args
    w, h = rect[2] - rect[0], rect[3] - rect[1]
    if w < 8 or h < 8:
        return set()
    out = "/tmp/rectpick_crop_%d.png" % os.getpid()
    vf = "crop=%d:%d:%d:%d" % (w, h, rect[0], rect[1])
    if scale > 1:
        vf += ",scale=iw*%d:ih*%d" % (scale, scale)
    subprocess.run(["ffmpeg", "-loglevel", "error", "-i", frame, "-vf", vf, "-y", out],
                   check=False, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    got = {norm(t) for t, *_ in tsv_words(out)}
    try:
        os.unlink(out)
    except OSError:
        pass
    return got


def main():
    walk, video = sys.argv[1], sys.argv[2]
    every, apply_, jobs = 2.0, False, 8
    name = os.path.basename(os.path.dirname(os.path.dirname(os.path.abspath(walk))))
    cache = os.path.join("/tmp/rectpick", name + "-" + os.path.basename(walk))
    args = sys.argv[3:]
    i = 0
    while i < len(args):
        if args[i] == "--every":
            every = float(args[i + 1]); i += 2
        elif args[i] == "--cache":
            cache = args[i + 1]; i += 2
        elif args[i] == "--jobs":
            jobs = int(args[i + 1]); i += 2
        elif args[i] == "--apply":
            apply_ = True; i += 1
        else:
            i += 1
    os.makedirs(cache, exist_ok=True)

    dur = probe(video)
    stamps = [round(t, 1) for t in _frange(0, dur, every)]
    with cf.ProcessPoolExecutor(max_workers=jobs) as ex:
        ts = [t for t in ex.map(extract, [(video, t, cache) for t in stamps]) if t is not None]
    todo = checks(walk)
    print("%s: %ds, %d samples, %d full-frame checks with words"
          % (os.path.basename(video), dur, len(ts), len(todo)))

    tsv_cache = os.path.join(cache, "tsv")
    os.makedirs(tsv_cache, exist_ok=True)
    with cf.ProcessPoolExecutor(max_workers=jobs) as ex:
        words_by_t = dict(zip(ts, ex.map(
            _cached_tsv, [(os.path.join(cache, "f%06d.jpg" % t), tsv_cache) for t in ts])))
    have_by_t = {}
    for t, ws in words_by_t.items():
        have = collections.defaultdict(list)
        for w in ws:
            have[norm(w[0])].append(w)
        have_by_t[t] = have

    lines = open(walk).read().split("\n")
    changed = 0
    at = 0
    for c in todo:
        if all(len(s) > 1 for s in c["sets"]):
            need = 1
        else:
            need = max(1, (len(c["sets"]) + 1) // 2)
            if len(c["sets"]) > 1:
                need = max(2, need)
        hit = None
        for ordered in ([t for t in ts if t >= at], ts):
            for t in ordered:
                have = have_by_t[t]
                present = [s for s in c["sets"] if all(have[norm(w)] for w in s)]
                if len(present) < need:
                    continue
                best = max(present, key=len)
                hit = (t, tightest([have[norm(w)] for w in best]), best)
                break
            if hit:
                break
        if hit is None:
            print("line %-4d NOT FOUND at or after %ds  %s" % (c["line"], at, c["sets"][:2]))
            continue
        t0, box, matched = hit
        rect = pad(box)
        for t in ts:
            if t < t0 - 6 or t > t0 + 6:
                continue
            have = have_by_t[t]
            if any(not have[norm(w)] for w in matched):
                continue
            for s in c["sets"]:
                occ = [have[norm(w)] for w in s]
                if any(not o for o in occ):
                    continue
                b = pad(tightest(occ))
                rect = [min(rect[0], b[0]), min(rect[1], b[1]),
                        max(rect[2], b[2]), max(rect[3], b[3])]

        if rect[3] > H - 60 or rect[1] < 40:
            print("line %-4d STAYS FULL FRAME  %s sits in a title or status bar (y %d..%d)"
                  % (c["line"], "+".join(matched), rect[1], rect[3]))
            at = t0 + 1
            continue
        area = (rect[2] - rect[0]) * (rect[3] - rect[1])
        if area > 0.55 * W * H:
            print("line %-4d STAYS FULL FRAME  box %dx%d covers %.0f%%"
                  % (c["line"], rect[2] - rect[0], rect[3] - rect[1], 100.0 * area / (W * H)))
            at = t0 + 1
            continue
        places = set()
        for t in ts:
            have = have_by_t[t]
            if not all(have[norm(w)] for w in matched):
                continue
            b = tightest([have[norm(w)] for w in matched])
            places.add((b[0] // 80, b[1] // 80, b[2] // 80, b[3] // 80))
            if len(places) > 1:
                break
        if len(places) > 1:
            print("line %-4d STAYS FULL FRAME  %s sits in %d distinct places"
                  % (c["line"], "+".join(matched), len(places)))
            at = t0 + 1
            continue

        probes = [os.path.join(cache, "f%06d.jpg" % t) for t in ts if abs(t - t0) <= 3][:6]
        want = {norm(w) for w in matched}
        best_scale, best_ok = None, 0
        for scale in (1, 2, 3):
            with cf.ProcessPoolExecutor(max_workers=jobs) as ex:
                reads = list(ex.map(crop_words, [(p, rect, scale) for p in probes]))
            ok = sum(1 for r in reads if want <= r)
            if ok > best_ok:
                best_scale, best_ok = scale, ok
            if ok == len(probes):
                break
        if best_scale is None:
            print("line %-4d NO SCALE READS  rect=%s  %s" % (c["line"], rect, c["sets"][:2]))
            continue
        flag = "" if best_ok == len(probes) else "  (%d/%d frames)" % (best_ok, len(probes))
        print("line %-4d rect=[%d, %d, %d, %d] %4dx%-4d scale=%d  t=%d  %s%s"
              % (c["line"], rect[0], rect[1], rect[2], rect[3],
                 rect[2] - rect[0], rect[3] - rect[1], best_scale, t0, "+".join(matched), flag))
        at = t0
        if apply_:
            old = lines[c["line"] - 1]
            new = old.replace("rect: [0, 0, 1920, 1080]",
                              "rect: [%d, %d, %d, %d]" % tuple(rect), 1)
            if "scale: 1," in new and best_scale != 1:
                new = new.replace("scale: 1,", "scale: %d," % best_scale, 1)
            if new != old:
                lines[c["line"] - 1] = new
                changed += 1
    if apply_:
        open(walk, "w").write("\n".join(lines))
        print("rewrote %d of %d checks in %s" % (changed, len(todo), walk))


def _frange(a, b, step):
    t = a
    while t < b:
        yield t
        t += step


def _cached_tsv(args):
    frame, cache = args
    key = os.path.join(cache, os.path.basename(frame) + ".json")
    if os.path.exists(key):
        return json.load(open(key))
    words = tsv_words(frame)
    json.dump(words, open(key, "w"))
    return words


if __name__ == "__main__":
    main()
