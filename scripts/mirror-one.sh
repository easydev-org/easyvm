#!/bin/sh
# MIRROR ONE ARTIFACT: download it, verify it against its pin, and push it under its tag.
#
#   scripts/mirror-one.sh <tag> <url> <sha256|sha1|sha512|empty> <bytes>
#
# ONE ARTIFACT PER INVOCATION, BECAUSE THAT IS ONE JOB. This was a `while` loop inside one job that did
# all fifteen, and the loop had two costs that a matrix does not:
#
#	ONE FAILURE STOPPED NOTHING BUT ALSO BLOCKED NOTHING - the loop collected names into `$failed`
#	and exited at the end, so a run that lost three downloads and pushed twelve looked, for two hours,
#	exactly like a run that was working
#	ONE RUNNER'S DISK held every file at some point, and the largest is 7.2 GiB
#
# AS A MATRIX JOB each artifact gets its own runner, its own disk, its own log and its own verdict. A
# failed one is re-run on its own with `gh run rerun --job`.
#
# EVERY ATTEMPT RESUMES INSTEAD OF RESTARTING, WHICH IS THE FIX FOR THE MEASUREMENT BELOW.
#
# `curl --retry` RETRIES FROM BYTE ZERO. For a 7.7 GiB Windows image on a mirror that resets every few
# minutes that is not a retry, it is the same failure five times: measured on `iso-blissos-16.9.7`,
# whose job spent ONE HUNDRED MINUTES inside this step and delivered nothing, while the same URL from
# the same commit took TWO MINUTES locally. Each `--retry` threw away everything it had.
#
# SO THE TRANSFER WRITES `<dest>.part` AND ASKS FOR THE REST, exactly as `.github/pull-artifact.sh`
# does for blobs the registry serves:
#
#	attempt N asks for bytes have.. and appends to the file already on disk
#
# and the file is only renamed when its size matches the pin. A vendor that ignores `Range` and sends
# 200 answers with the whole file, which `curl -C -` truncates rather than appending the start to the
# end - so a mirror without range support still works, it just does not get faster.
set -eu

tag=${1:?tag}
url=${2:?url}
sha=${3:-}
size=${4:-0}

repository=${DISKS_REPOSITORY:-${GITHUB_REPOSITORY:-easydev-org/easyvm}}
repository=$(printf '%s' "$repository" | tr '[:upper:]' '[:lower:]')

# THE FILE HAS TO OUTLIVE THIS SCRIPT, because the next STEP pushes it - so the directory is created
# here and NOT removed on exit. The runner is torn down after the job, which is when it goes.
work=${RUNNER_TEMP:-/tmp}/mirror
mkdir -p "$work"
dest="$work/$tag"
part="$dest.part"

echo "=== $tag" >&2
echo "  group ${GROUP:-any}  url $url" >&2
echo "  pin ${sha:-(none - size floor only)}  expected ${size:-0} bytes" >&2

# A FILE THAT ARRIVED IS KEPT BY SIZE, NOT BY EXISTENCE. A truncated transfer from an earlier run
# would otherwise look complete forever, and the failure would surface as a corrupt ISO much later.
if [ -f "$dest" ]; then
    have=$(stat -c %s "$dest")
    if [ "${size:-0}" -gt 0 ] && [ "$have" -eq "$size" ]; then
        echo "  $dest already complete ($have bytes); not downloading again" >&2
    else
        echo "  $dest is $have bytes and the catalogue says $size; discarding it" >&2
        rm -f "$dest"
    fi
fi

# HOW MANY TIMES TO ASK, AND WHY IT IS NOT FIVE.
#
# Every attempt now makes progress, so the ceiling is about a mirror that stalls repeatedly rather
# than about how long the file is. Twelve attempts on a 7.7 GiB image is twelve chances to collect the
# last few megabytes a flaky CDN refuses to send in one go; the cost of an extra attempt is one
# request, and the cost of stopping early is the whole download.
attempts=${MIRROR_ATTEMPTS:-12}
attempt=0
while [ "$attempt" -lt "$attempts" ]; do
    attempt=$((attempt + 1))

    have=0
    [ -f "$part" ] && have=$(stat -c %s "$part")
    if [ "${size:-0}" -gt 0 ] && [ "$have" -gt "$size" ]; then
        # A part larger than the file wrote past the end - a redirect to an error page, say - and
        # cannot be resumed from.
        echo "  discarding an oversized part ($have > $size)" >&2
        rm -f "$part"
        have=0
    fi
    if [ "${size:-0}" -gt 0 ] && [ "$have" -eq "$size" ]; then
        break
    fi

    if [ "$have" -gt 0 ]; then
        echo "=== attempt $attempt/$attempts: resuming at ${have} bytes" >&2
    else
        echo "=== attempt $attempt/$attempts: from the start" >&2
    fi

    # NO `--max-time`: it limits the WHOLE transfer, so it ends a 7.2 GiB download at the number
    # whatever the speed. The connect timeout guards a dead mirror. `--retry` IS STILL HERE and is now
    # harmless: it retries within one attempt, and whichever way it ends the file on disk keeps what
    # arrived, because `-C -` asks for the rest on the next pass.
    #
    # `-C -` TELLS CURL TO RESUME FROM THE FILE IT IS WRITING. When the server answers 200 instead of
    # 206 curl starts the file over, which is correct - it does not append a second copy to the first.
    # `--no-progress-meter` AND A LINE OF OUR OWN. The default progress bar reprints the whole
    # transfer every second, which on a 7.7 GiB image is thousands of log lines nobody reads - and the
    # Actions log is where a reader looks for the ONE line that says what happened. `%{size_download}`
    # at the end is the number that matters: how much this attempt added.
    if curl -fL -C - --retry 3 --retry-delay 5 --retry-all-errors \
            --connect-timeout 30 --no-progress-meter \
            -w '  this attempt: %{size_download} bytes in %{time_total}s (%{speed_download} B/s)\n' \
            -o "$part" "$url" >&2; then
        got=$(stat -c %s "$part" 2>/dev/null || echo 0)
        # A MIRROR THAT SERVES A PARTIAL BODY WITHOUT FAILING is why the size is checked here and not
        # only after the loop: `curl` returns 0 for a connection closed after the headers.
        if [ "${size:-0}" -gt 0 ] && [ "$got" -lt "$size" ]; then
            echo "  got $got of $size bytes; asking for the rest" >&2
            continue
        fi
        break
    else
        echo "  the transfer was cut; the partial file keeps what arrived" >&2
    fi
    # A PAUSE BEFORE ASKING AGAIN, because a mirror that just reset is usually a mirror that is about
    # to reset for the same reason - and hammering it is how a temporary refusal becomes a ban.
    sleep 5
done

got=$(stat -c %s "$part" 2>/dev/null || echo 0)
if [ "${size:-0}" -gt 0 ] && [ "$got" -ne "$size" ]; then
    echo "::error::$tag: $attempt attempt(s) and $got of $size bytes" >&2
    echo "::error::every attempt resumed from the partial file, so the mirror is refusing the bytes" >&2
    echo "::error::rather than resetting the stream. The .part file is left in place." >&2
    exit 1
fi
mv "$part" "$dest"

got=$(stat -c %s "$dest")
echo "  got $got bytes" >&2

# THE PIN IS THE POINT. A file that does not match is not a newer version - it is the WRONG FILE, and
# pushing it would put it behind the right tag where nobody looks.
#
# THE PIN'S LENGTH SAYS WHICH DIGEST TO COMPUTE. Apple publishes a sha1 for the Xcode archive,
# opensuse publishes a sha512 and no sha256 at all (its `.sha256` is a 404), and everything else is
# sha256. CHECKING EVERYTHING AS SHA256 REFUSED THE CORRECT XCODE FILE:
#
#	warning: macos-asset-xcode-26.3: sha256 is cf87232e..., the pin is cebf05ec...
#
# and `cebf05ec...` is 40 hex digits - a sha1 - being compared against 64.
if [ -n "$sha" ] && [ "$sha" != "0" ]; then
    case ${#sha} in
        40) algo=sha1 ;;
        64) algo=sha256 ;;
        128) algo=sha512 ;;
        *) echo "::error::$tag: the pin is ${#sha} hex digits, which is not sha1, sha256 or sha512"
           exit 1 ;;
    esac
    actual=$("${algo}sum" "$dest" | awk '{print $1}')
    if [ "$actual" != "$sha" ]; then
        echo "::error::$tag: ${algo} is $actual, the pin is $sha"
        exit 1
    fi
    echo "  $algo matches" >&2
fi

# A SIZE FLOOR FOR THE UNPINNED ONES. virtio-win and a couple of mirrors rotate under a fixed URL, so
# they carry no hash - and a 404 page that arrived with 200 would otherwise be pushed as an artifact.
if [ "${size:-0}" -gt 0 ]; then
    [ "$got" -eq "$size" ] || echo "  (note: $got bytes, the catalogue says $size)" >&2
fi

# THE PATH GOES TO STDOUT AND EVERYTHING ELSE TO STDERR, because a shell returns a value on stdout and
# a caller that captured the log instead would try to push a sentence.
echo "  ready to push: $dest" >&2
printf '%s\n' "$dest"
