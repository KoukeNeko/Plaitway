#!/usr/bin/env bash
# Renders the notes of a release: what changed (releases/X.Y.Z.md), how to install it
# (packaging/release-notes.md) and the checksums of its files.
#
#   scripts/render-release-notes.sh v0.3.0 SHA256SUMS > notes.md
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
version="${1#v}"
sums="$2"
changes="$root/releases/$version.md"
[[ -f "$changes" ]] || { echo "$changes is missing: write what changed in $version first" >&2; exit 1; }

cat "$changes"
printf '\n'
cat "$root/packaging/release-notes.md"
printf '\n## Checksums (SHA-256)\n\n```\n'
cat "$sums"
printf '```\n'
