#!/bin/bash
# Produces build/Plaitway-<version>.zip and .dmg from build/Plaitway.app.
# The dmg holds the app and an Applications symlink for drag-and-drop install.
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

read_version
APP="$BUILD_DIR/Plaitway.app"
ZIP="$BUILD_DIR/Plaitway-$VERSION.zip"
DMG="$BUILD_DIR/Plaitway-$VERSION.dmg"
DMG_STAGE="$BUILD_DIR/stage/dmg"

[ -d "$APP" ] || die "$APP is missing; run packaging/package-app.sh (make app)"

log "creating $ZIP"
rm -f "$ZIP"
# Without --norsrc ditto stores extended attributes (the build host's
# com.apple.provenance among them) as AppleDouble ._* entries, which unzip and
# most other extractors write into the bundle, where they break its signature.
ditto -c -k --norsrc --noextattr --noqtn --noacl --keepParent "$APP" "$ZIP"
check_zip_matches_app "$ZIP" "$APP" || die "$ZIP is not a faithful copy of $APP"

log "creating $DMG"
rm -rf "$DMG_STAGE" "$DMG"
mkdir -p "$DMG_STAGE"
ditto "$APP" "$DMG_STAGE/Plaitway.app"
ln -s /Applications "$DMG_STAGE/Applications"
hdiutil create -quiet -volname Plaitway -srcfolder "$DMG_STAGE" -format UDZO -ov "$DMG"
rm -rf "$DMG_STAGE"
sign_disk_image "$DMG"
codesign --verify --strict --verbose=2 "$DMG"

log "done: $ZIP $DMG"
