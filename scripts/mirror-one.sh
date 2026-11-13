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

echo "=== $tag" >&2
echo "  group ${GROUP:-any}  url $url" >&2
echo "  pin ${sha:-(none - size floor only)}  expected ${size:-0} bytes" >&2

# NO `--max-time`: it limits the WHOLE transfer, so it ends a 7.2 GiB download at the number whatever
# the speed. The connect timeout guards a dead mirror; `--retry-all-errors` retries a stall.
if ! curl -fL --retry 5 --retry-delay 10 --retry-all-errors \
        --connect-timeout 30 -o "$dest" "$url"; then
    echo "::error::$tag: download failed from $url"
    exit 1
fi


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
