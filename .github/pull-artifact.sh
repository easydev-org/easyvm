#!/bin/sh
# PULL ONE PUBLISHED ARTIFACT, RESUMING ACROSS RESETS INSTEAD OF RESTARTING.
#
#   .github/pull-artifact.sh <registry> <repository> <tag> <dest> [attempts]
#
# WHY THIS IS NOT `oras pull`, WHICH IS WHAT IT REPLACED
#
# `oras pull` HAS NO RESUME. A transfer reset at the other end starts again from byte zero, and a
# multi-gigabyte blob over a long-lived HTTP/2 stream is reset often enough that several attempts can
# all fail without making net progress. MEASURED, FROM THE REFERENCE IMPLEMENTATION'S OWN LOG, on a
# 28 GiB blob:
#
#	attempt 1  03:39:28 -> 03:45:00   stream error: stream ID 1; PROTOCOL_ERROR
#	attempt 2  03:45:05 -> 03:48:58   the same
#	attempt 3  03:49:08 -> 03:55:03   the same
#	attempt 4  03:55:18 -> ...        the same
#
# Three attempts of five to six minutes each, and the file never got past the first. A retry around
# `oras pull` would reproduce exactly that: each attempt restarts at zero, so more attempts do not
# make progress, they make more failures.
#
# SO THE BLOB IS FETCHED DIRECTLY WITH `Range:`. The registry is a normal OCI distribution endpoint,
# so three calls are all it takes: a token, a manifest (for the digest and the size), and the blob.
# The transfer writes `<dest>.part` and asks for the rest on the next attempt.
#
# WHAT IT DOES NOT DO: no fallback to another tag or registry. The caller names one artifact, and "the
# pull failed" is a fact worth reporting rather than hiding.
set -eu

registry=${1:?registry}
repo=${2:?repository}
tag=${3:?tag}
dest=${4:?dest}
attempts=${5:-8}

[ -n "${GITHUB_TOKEN:-}" ] || {
    echo "::error::GITHUB_TOKEN is empty; a private package needs it and an anonymous pull answers 401"
    exit 1
}

part="$dest.part"
mkdir -p "$(dirname "$dest")"

echo "=== token for $repo"
token=$(curl -fsS --retry 3 --max-time 120 \
    "https://$registry/token?scope=repository:$repo:pull&service=$registry" \
    -u "x:$GITHUB_TOKEN" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
[ -n "$token" ] || { echo "::error::no registry token for $repo"; exit 1; }

echo "=== manifest $registry/$repo:$tag"
manifest=$(curl -fsS --retry 3 --max-time 120 \
    -H "Authorization: Bearer $token" \
    -H "Accept: application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json" \
    "https://$registry/v2/$repo/manifests/$tag") || {
    echo "  (no $tag in the package)"
    exit 1
}

digest=$(printf '%s' "$manifest" | sed -n 's/.*"digest":"\(sha256:[a-f0-9]*\)".*/\1/p' | head -1)
size=$(printf '%s' "$manifest" | sed -n 's/.*"size":\([0-9]*\).*/\1/p' | head -1)
[ -n "$digest" ] && [ -n "$size" ] || {
    echo "::error::the manifest for $tag has no digest or size"
    exit 1
}
echo "  layer $digest, $size bytes"

# A FILE THAT ARRIVED IS SKIPPED BY SIZE, NOT BY EXISTENCE. A truncated transfer from an earlier run
# would otherwise look complete forever, and the failure would surface as a corrupt disk much later.
if [ -f "$dest" ]; then
    have=$(stat -c %s "$dest")
    if [ "$have" = "$size" ]; then
        echo "  $dest already complete ($have bytes)"
        exit 0
    fi
    echo "  $dest is $have bytes but the manifest says $size; discarding it"
    rm -f "$dest"
fi

url="https://$registry/v2/$repo/blobs/$digest"
attempt=0
while [ "$attempt" -lt "$attempts" ]; do
    attempt=$((attempt + 1))
    have=0
    [ -f "$part" ] && have=$(stat -c %s "$part")

    if [ "$have" = "$size" ]; then
        mv "$part" "$dest"
        echo "  $dest complete ($size bytes)"
        exit 0
    fi
    if [ "$have" -gt "$size" ]; then
        # A part larger than the blob wrote past the end (a redirect to an error page, say) and
        # cannot be resumed from.
        echo "  discarding an oversized part ($have > $size)"
        rm -f "$part"
        have=0
    fi

    echo "=== attempt $attempt/$attempts: ${have} of ${size} bytes"
    # `-C -` RESUMES FROM THE .part FILE THIS SCRIPT OWNS. `Range` is what the registry answers with
    # 206; if it ignores the header and returns 200 the whole blob arrives, which is still correct -
    # curl truncates rather than appending the start to the end.
    if curl -fL --retry 2 --retry-delay 5 \
            -H "Authorization: Bearer $token" \
            -C - -o "$part" "$url"; then
        got=$(stat -c %s "$part" 2>/dev/null || echo 0)
        if [ "$got" = "$size" ]; then
            mv "$part" "$dest"
            echo "  $dest complete ($size bytes)"
            exit 0
        fi
        echo "  got $got of $size bytes"
    else
        echo "  the transfer was cut; the partial file keeps what arrived"
    fi
done

echo "::error::$tag: $attempts attempts and the blob is $(stat -c %s "$part" 2>/dev/null || echo 0) of $size bytes"
echo "::error::the downloader resumed from a partial file on every attempt, so this is the registry"
echo "::error::refusing the blob rather than resetting the stream."
exit 1
