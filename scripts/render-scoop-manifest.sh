#!/usr/bin/env bash
# Renders the Scoop manifest for a published release.
#
#   scripts/render-scoop-manifest.sh v0.2.0 SHA256SUMS > plaitway.json
set -euo pipefail

tag="$1"
sums="$2"
version="${tag#v}"
sha_for() {
  local asset="$1" sha
  sha="$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1 }' "$sums")"
  [[ ${#sha} -eq 64 ]] || { echo "no SHA-256 for $asset in $sums" >&2; exit 1; }
  echo "$sha"
}
amd64="$(sha_for "Plaitway-$version-windows-amd64.zip")"
arm64="$(sha_for "Plaitway-$version-windows-arm64.zip")"
base="https://github.com/KoukeNeko/Plaitway/releases/download"

cat <<MANIFEST
{
    "version": "$version",
    "description": "Run several OpenVPN and WireGuard VPNs at once",
    "homepage": "https://github.com/KoukeNeko/Plaitway",
    "license": "MIT",
    "notes": "Windows support is in development: plaitwayd serves an in-memory backend only (plaitwayd -fake), and there is no app yet.",
    "architecture": {
        "64bit": {
            "url": "$base/v$version/Plaitway-$version-windows-amd64.zip",
            "hash": "$amd64"
        },
        "arm64": {
            "url": "$base/v$version/Plaitway-$version-windows-arm64.zip",
            "hash": "$arm64"
        }
    },
    "bin": [
        "plaitway.exe",
        "plaitwayd.exe"
    ],
    "checkver": {
        "github": "https://github.com/KoukeNeko/Plaitway"
    },
    "autoupdate": {
        "architecture": {
            "64bit": {
                "url": "$base/v\$version/Plaitway-\$version-windows-amd64.zip"
            },
            "arm64": {
                "url": "$base/v\$version/Plaitway-\$version-windows-arm64.zip"
            }
        },
        "hash": {
            "url": "$base/v\$version/SHA256SUMS"
        }
    }
}
MANIFEST
