#!/usr/bin/env bash
# Renders the Scoop manifest for a published release.
#
#   scripts/render-scoop-manifest.sh v0.6.0 SHA256SUMS-windows > plaitway.json
#
# Scoop would unpack an .msi instead of installing it, so the download is stored
# under another name (#/Plaitway.dl) and the installer script runs msiexec on it,
# elevated, because the package registers a service.
set -euo pipefail

tag="$1"
sums="$2"
version="${tag#v}"
asset="Plaitway-$version-x64-en-US.msi"
sha="$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1 }' "$sums")"
[[ ${#sha} -eq 64 ]] || { echo "no SHA-256 for $asset in $sums" >&2; exit 1; }

template="$(cat <<'JSON'
{
    "version": "@VERSION@",
    "description": "Run several OpenVPN and WireGuard VPNs at once",
    "homepage": "https://github.com/KoukeNeko/Plaitway",
    "license": "MIT",
    "notes": [
        "Installing and removing ask Windows for administrator rights, because the package registers the service PlaitwayHelper.",
        "The command line client is C:\\Program Files\\Plaitway\\plaitway.exe. Removing Plaitway keeps %ProgramData%\\Plaitway, which holds the profiles."
    ],
    "architecture": {
        "64bit": {
            "url": "https://github.com/KoukeNeko/Plaitway/releases/download/v@VERSION@/Plaitway-@VERSION@-x64-en-US.msi#/Plaitway.dl",
            "hash": "@SHA@"
        }
    },
    "installer": {
        "script": [
            "$msi = \"$dir\\Plaitway.msi\"",
            "Move-Item \"$dir\\Plaitway.dl\" $msi",
            "$run = Start-Process msiexec.exe -ArgumentList \"/i `\"$msi`\" /qb\" -Verb RunAs -Wait -PassThru",
            "if ($run.ExitCode -notin 0, 3010) { throw \"The Plaitway installer failed with exit code $($run.ExitCode).\" }"
        ]
    },
    "uninstaller": {
        "script": [
            "$run = Start-Process msiexec.exe -ArgumentList \"/x `\"$dir\\Plaitway.msi`\" /qb\" -Verb RunAs -Wait -PassThru",
            "if ($run.ExitCode -notin 0, 1605, 3010) { throw \"The Plaitway uninstaller failed with exit code $($run.ExitCode).\" }"
        ]
    },
    "checkver": {
        "github": "https://github.com/KoukeNeko/Plaitway"
    },
    "autoupdate": {
        "architecture": {
            "64bit": {
                "url": "https://github.com/KoukeNeko/Plaitway/releases/download/v$version/Plaitway-$version-x64-en-US.msi#/Plaitway.dl"
            }
        },
        "hash": {
            "url": "https://github.com/KoukeNeko/Plaitway/releases/download/v$version/SHA256SUMS-windows"
        }
    }
}
JSON
)"
template="${template//@VERSION@/$version}"
printf '%s\n' "${template//@SHA@/$sha}"
