#!/bin/sh
# STREAM A COMMAND'S NEWEST OUTPUT TO A CHECK RUN, WITH NO DEPENDENCY BEYOND curl.
#
#   .github/log-stream.sh --name my-leg --interval 10 --max-bytes 60000 -- <command...>
#
# THE PROBLEM IS GITHUB'S, NOT OURS. A step's Actions log is unreadable until the step finishes, and a
# leg spends up to three hours inside one step - so "which step is it on" is answered only after the
# answer has stopped mattering. A CHECK RUN's `output.text` IS readable while it runs.
#
# THE ID IS READ OUT OF THE RESPONSE WITH `sed`, WHICH IS WHY THE SPACE MATTERS: GitHub answers
# `{"id": 4242, ...}` with a space after the colon, and the first version of this regex required none.
# The check run was created - the POST succeeded - and the script then decided it had failed and ran
# the command unpublished, which is the one failure mode that looks like "the feature does nothing".
#
# TWO REQUESTS, AND THAT IS THE WHOLE PROTOCOL:
#
#	POST  /repos/$REPO/check-runs          once, status=in_progress -> capture the id
#	PATCH /repos/$REPO/check-runs/$ID      output.text every interval, conclusion at the end
#
# NO PYTHON, NO jq, NO gh. The image deliberately carries no interpreter - the driver is Go and
# speaks QMP directly - and `jq` is not in it either, so the id is read with `sed` from a response this
# script generated itself. THE ENTIRE DEPENDENCY IS `curl`, which is already here because `prepare`
# fetches oras with it.
#
# AND IT IS CANCELLABLE. The command runs under `setsid`, so its pid is its process group and the trap
# kills `-- -$pid`: that reaches qemu's children, where `kill $pid` reaches only the shell and orphans
# the machine. The Python implementation learned this the hard way - "the log stops" and "the machine
# stops" are different outcomes.
set -eu

name=${GITHUB_JOB:-easyvm}
interval=10
max_bytes=60000
token=${GITHUB_TOKEN:-}
api=${GITHUB_API_URL:-https://api.github.com}
repo=${GITHUB_REPOSITORY:-}
sha=${GITHUB_SHA:-}

while [ $# -gt 0 ]; do
    case "$1" in
        --name)       name=$2; shift 2 ;;
        --interval)   interval=$2; shift 2 ;;
        --max-bytes)  max_bytes=$2; shift 2 ;;
        --token)      token=$2; shift 2 ;;
        --)           shift; break ;;
        *)            break ;;
    esac
done

[ $# -gt 0 ] || { echo "log-stream: a command is required after --" >&2; exit 2; }
[ -n "$token" ] || { echo "log-stream: no token; running the command without publishing" >&2
                     exec "$@"; }
[ -n "$repo" ] || { echo "log-stream: GITHUB_REPOSITORY is not set" >&2; exit 2; }
[ -n "$sha" ] || { echo "log-stream: GITHUB_SHA is not set" >&2; exit 2; }

# GITHUB REFUSES MORE THAN 65535 CHARACTERS, so the cap is not a preference.
[ "$max_bytes" -gt 65000 ] && max_bytes=65000

# json_escape turns a log into the inside of a JSON string.
#
# THE ORDER IS THE WHOLE THING: THE BACKSLASH FIRST, THEN THE QUOTE. Reversed, the backslash this
# adds before a quote would itself be escaped, and `a"b` would arrive as `a\\\"b` - which GitHub
# accepts and a reader sees as garbage. TABS AND CONTROL CHARACTERS ARE DROPPED rather than escaped,
# because a log is the one place they are always noise; a raw one inside a JSON string is a parse
# error for the whole request, which is how one bell character would silently stop all publishing.
json_escape() {
    # THE NEWLINE IS THE ONE THAT BITES. A JSON string may not contain a raw control character, and a
    # log is mostly newlines - so they are turned into the two-character escape `\n` and the trailing
    # newline is dropped (a log that ends in a newline would otherwise publish a blank last line).
    #
    # MEASURED: the first version removed control characters with `tr -d '\000-\010\013\014\016-\037'`,
    # which deliberately kept `\012` because the author was thinking of tabs - and Python refused the
    # payload with `Invalid control character at: line 1 column 60`. GitHub would have refused the same
    # request, silently, once per interval, for the whole run.
    #
    # THE TAB IS THE SECOND ONE, AND IT IS KEPT ON PURPOSE: a tab in a log means an aligned column, so
    # it is escaped rather than deleted - `tr` keeps `\011` and the last `sed` turns it into `\t`.
    #
    # THE ORDER IS STILL: BACKSLASH, THEN QUOTE. The newline is escaped after the others because `sed`
    # reads one line at a time and cannot see one until the lines are joined.
    printf '%s' "$1" \
        | tr -d '\000-\010\013\014\016-\037' \
        | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' \
        | sed -e ':a' -e 'N' -e '$!ba' -e 's/\n/\\n/g' -e 's/\t/\\t/g'
}

work=$(mktemp -d)
log="$work/log"
: > "$log"
trap 'rm -rf "$work"' EXIT

# --- the check run ------------------------------------------------------------------------------
#
# THE JSON IS WRITTEN BY A HERE-DOCUMENT WITH THE VALUES SUBSTITUTED, because there is no jq. Every
# value here is a name this script was given, not user data, so there is nothing to escape wrongly -
# and `name` is quoted for the one case that matters (a leg name containing a space).
id=$(curl -fsS -X POST \
    -H "Authorization: Bearer $token" \
    -H "Accept: application/vnd.github+json" \
    -H "Content-Type: application/json" \
    "$api/repos/$repo/check-runs" \
    -d "{\"name\":\"$name\",\"head_sha\":\"$sha\",\"status\":\"in_progress\"}" \
    2>/dev/null | sed -n 's/.*"id"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' | head -1) || id=""

if [ -z "$id" ]; then
    # A STREAM THAT CANNOT PUBLISH STILL RUNS THE COMMAND. An API outage must not fail a leg: the log
    # is a convenience and the work is the point.
    echo "log-stream: could not create a check run; running without publishing" >&2
    exec "$@"
fi

echo "log-stream: watching at $api/repos/$repo/check-runs/$id" >&2

# --- the command ---------------------------------------------------------------------------------
#
# A PLAIN BACKGROUND JOB, AND THE CANCEL WALKS THE TREE.
#
# THREE ATTEMPTS LANDED HERE, AND THE FAILURES ARE THE REASON IT IS THIS SIMPLE:
#
#	`set -m`            prints `can't access tty; job control turned off` in a non-interactive shell,
#	                    and a shell OPTION is not a property of the child - the runner decides.
#	`setsid`            forks and exits, so `$!` is the wrapper and `wait` returns at once: the stream
#	                    would report success while the leg was still installing.
#	`setsid --wait`     is not on every host, and where it is missing the fallback is the same fork.
#
# SO THE COMMAND IS A NORMAL BACKGROUND JOB and the trap kills its DESCENDANTS itself. `pkill -P` is
# in procps, which this image already has for `pgrep`; the loop below walks two levels, which covers
# shell -> qemu and shell -> easyvm -> qemu. It is not a general process-tree kill and does not pretend
# to be: it is enough to stop a machine, and the last resort is the runner killing the whole container.
"$@" > "$log" 2>&1 &
pid=$!

child=0
# THE TREE, NOT THE PROCESS. `kill $pid` reaches the shell that owns the daemon; the daemon owns qemu.
kill_tree() {
    for child in $(pgrep -P "$1" 2>/dev/null || true); do
        kill_tree "$child"
    done
    kill "$1" 2>/dev/null || true
}
trap 'child=1; kill_tree "$pid"' TERM INT HUP

# --- publishing ----------------------------------------------------------------------------------
#
# `tail -c` FROM A FILE RATHER THAN A PIPE, so the leader never blocks the command and the newest
# bytes are cheap to take however much has been written.
publish() {
    body=$(tail -c "$max_bytes" "$log" 2>/dev/null || true)
    [ -n "$body" ] || body="(no output yet)"
    # THE JSON IS BUILT BY sed RATHER THAN BY A QUOTER, because the only characters that break a JSON
    # string are the backslash, the quote and the control characters - and a log is full of the first
    # two. Order matters: backslash first, or it would escape the quotes this adds.
    escaped=$(json_escape "$body")
    curl -fsS -X PATCH \
        -H "Authorization: Bearer $token" \
        -H "Accept: application/vnd.github+json" \
        -H "Content-Type: application/json" \
        "$api/repos/$repo/check-runs/$id" \
        -d "{\"output\":{\"title\":\"$name\",\"summary\":\"$(tail -c "$max_bytes" "$log" | wc -c) bytes\",\"text\":\"$escaped\"}}" \
        >/dev/null 2>&1 || true
}

while kill -0 "$pid" 2>/dev/null; do
    publish
    # THE SLEEP IS THE INTERVAL, BUT IT MUST NOT DELAY THE EXIT: `wait` cannot be interrupted in POSIX
    # sh, so the loop sleeps in one-second steps and notices the child each time.
    slept=0
    while [ "$slept" -lt "$interval" ] && kill -0 "$pid" 2>/dev/null; do
        sleep 1
        slept=$((slept + 1))
    done
done

wait "$pid"
code=$?
trap - TERM INT HUP

publish

conclusion=success
[ "$code" -eq 0 ] || conclusion=failure
body=$(tail -c "$max_bytes" "$log" 2>/dev/null || true)
escaped=$(json_escape "$body")
curl -fsS -X PATCH \
    -H "Authorization: Bearer $token" \
    -H "Accept: application/vnd.github+json" \
    -H "Content-Type: application/json" \
    "$api/repos/$repo/check-runs/$id" \
    -d "{\"status\":\"completed\",\"conclusion\":\"$conclusion\",\"output\":{\"title\":\"$name\",\"summary\":\"exit $code\",\"text\":\"$escaped\"}}" \
    >/dev/null 2>&1 || true

# AND THE LOG ALSO GOES TO THE STEP'S OWN STDOUT, so the Actions log still has it after the run - the
# check run is for watching, the artifact is for reading afterwards.
cat "$log"

if [ "$child" = "1" ]; then
    exit 1
fi
exit "$code"
