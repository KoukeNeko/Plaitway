#!/bin/bash
# Notarizes build/Plaitway.app with Apple's notary service, staples the ticket
# and rebuilds the zip and dmg around the stapled app.
#
# Needs a Developer ID signed app (make app) and a notarytool keychain profile:
#   xcrun notarytool store-credentials plaitway-notary --apple-id ... --team-id ...
# PLAITWAY_NOTARY_PROFILE overrides the profile name.
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

PROFILE="${PLAITWAY_NOTARY_PROFILE:-plaitway-notary}"
read_version
APP="$BUILD_DIR/Plaitway.app"
APP_ZIP="$BUILD_DIR/stage/Plaitway-notarize.zip"
DMG="$BUILD_DIR/Plaitway-$VERSION.dmg"

[ -d "$APP" ] || die "$APP is missing; run make app first"
# Captured first: grep -q exiting early would SIGPIPE codesign under pipefail.
signature="$(codesign -dvv "$APP" 2>&1)"
grep -q '^Authority=Developer ID Application' <<<"$signature" ||
    die "$APP is not signed with a Developer ID Application certificate; notarization would reject it"
xcrun notarytool history --keychain-profile "$PROFILE" >/dev/null 2>&1 ||
    die "keychain profile '$PROFILE' is missing or rejected; create it with: xcrun notarytool store-credentials $PROFILE"

# submit FILE: uploads FILE, waits for the verdict and prints the notary log
# when the submission is not accepted.
submit() {
    local result status id
    result="$(xcrun notarytool submit "$1" --keychain-profile "$PROFILE" --wait --output-format json)"
    status="$(printf '%s' "$result" | plutil -extract status raw -o - -)"
    id="$(printf '%s' "$result" | plutil -extract id raw -o - -)"
    if [ "$status" != Accepted ]; then
        xcrun notarytool log "$id" --keychain-profile "$PROFILE" >&2 || true
        die "notarization of $1 ended as '$status' (submission $id)"
    fi
    log "$(basename "$1") accepted (submission $id)"
}

log "notarizing the app"
mkdir -p "$(dirname "$APP_ZIP")"
rm -f "$APP_ZIP"
ditto -c -k --keepParent "$APP" "$APP_ZIP"
submit "$APP_ZIP"
xcrun stapler staple "$APP"
xcrun stapler validate "$APP"
spctl --assess --type execute --verbose=4 "$APP"

log "rebuilding the zip and dmg around the stapled app"
"$PACKAGING_DIR/make-dist.sh"

log "notarizing the dmg"
submit "$DMG"
xcrun stapler staple "$DMG"
xcrun stapler validate "$DMG"

log "done: $BUILD_DIR/Plaitway-$VERSION.zip $DMG"
