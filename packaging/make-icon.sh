#!/bin/bash
# Makes Plaitway.icns from the artwork in packaging/icon/Plaitway-1024.png: a 1024 pixel
# transparent canvas with the margin the macOS icon grid leaves around the artwork.
#
#   packaging/make-icon.sh OUTPUT.icns
#
# Each size of the iconset is scaled from the 1024 pixel original, not from the next size up.
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

SOURCE="$PACKAGING_DIR/icon/Plaitway-1024.png"
[ $# -eq 1 ] || die "usage: make-icon.sh OUTPUT.icns"
[ -f "$SOURCE" ] || die "$SOURCE is missing"
[ "$(sips -g pixelWidth "$SOURCE" | awk '/pixelWidth/ {print $2}')" = 1024 ] || die "$SOURCE is not 1024 pixels wide"
[ "$(sips -g hasAlpha "$SOURCE" | awk '/hasAlpha/ {print $2}')" = yes ] || die "$SOURCE has no transparency"

ICONSET="$(mktemp -d)/Plaitway.iconset"
mkdir -p "$ICONSET"
trap 'rm -rf "$(dirname "$ICONSET")"' EXIT

# name:pixels
for entry in icon_16x16:16 icon_16x16@2x:32 icon_32x32:32 icon_32x32@2x:64 icon_128x128:128 \
    icon_128x128@2x:256 icon_256x256:256 icon_256x256@2x:512 icon_512x512:512 icon_512x512@2x:1024; do
    name="${entry%%:*}" pixels="${entry##*:}"
    sips --resampleHeightWidth "$pixels" "$pixels" "$SOURCE" --out "$ICONSET/$name.png" >/dev/null
done
iconutil --convert icns "$ICONSET" --output "$1"
