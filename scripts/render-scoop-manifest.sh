#!/usr/bin/env bash
# Render the Scoop manifest for a published release.
#
#   scripts/render-scoop-manifest.sh v0.1.2 SHA256SUMS > sharecodex.json
set -euo pipefail

tag="$1"
sums="$2"
version="${tag#v}"
asset="ShareCodex-windows-amd64.zip"
sha="$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1 }' "$sums")"
[[ ${#sha} -eq 64 ]] || { echo "no SHA-256 for $asset in $sums" >&2; exit 1; }

cat <<MANIFEST
{
    "version": "$version",
    "description": "Track shared Claude Code and Codex subscription quota",
    "homepage": "https://github.com/KoukeNeko/ShareCodex",
    "architecture": {
        "64bit": {
            "url": "https://github.com/KoukeNeko/ShareCodex/releases/download/v$version/$asset",
            "hash": "$sha"
        }
    },
    "shortcuts": [
        [
            "ShareCodex.exe",
            "ShareCodex"
        ]
    ],
    "checkver": {
        "github": "https://github.com/KoukeNeko/ShareCodex"
    },
    "autoupdate": {
        "architecture": {
            "64bit": {
                "url": "https://github.com/KoukeNeko/ShareCodex/releases/download/v\$version/$asset"
            }
        },
        "hash": {
            "url": "https://github.com/KoukeNeko/ShareCodex/releases/download/v\$version/SHA256SUMS"
        }
    }
}
MANIFEST
