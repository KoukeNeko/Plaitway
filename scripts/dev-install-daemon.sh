#!/bin/bash
# Fallback for when SMAppService approval is not available: installs plaitwayd
# as a plain LaunchDaemon running from a root-owned copy of the app bundle.
#
#   sudo scripts/dev-install-daemon.sh [--app PATH] [--yes] [--dry-run]
#
# Safe to run again: it replaces the copy and restarts the job. Undo it with
# scripts/dev-uninstall-daemon.sh.
set -euo pipefail
# shellcheck source=../packaging/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/../packaging/lib.sh"

APP="$BUILD_DIR/Plaitway.app"
[ -d "$APP" ] || APP=/Applications/Plaitway.app
DRY_RUN=0
ASSUME_YES=0
while [ $# -gt 0 ]; do
    case "$1" in
        --app) [ $# -ge 2 ] || die "--app needs a path"; APP="$2"; shift 2 ;;
        --yes) ASSUME_YES=1; shift ;;
        --dry-run) DRY_RUN=1; shift ;;
        *) die "usage: sudo $0 [--app PATH] [--yes] [--dry-run]" ;;
    esac
done

SOURCE_PLIST="$APP/Contents/Library/LaunchDaemons/$DAEMON_LABEL.plist"
[ -x "$APP/Contents/MacOS/plaitwayd" ] || die "$APP has no Contents/MacOS/plaitwayd; build it with make app"
[ -f "$SOURCE_PLIST" ] || die "$SOURCE_PLIST is missing"

cat <<PLAN
This will:
  1. copy $APP
     to $DEV_APP_DIR, owned by root and not writable by others;
  2. write $DEV_PLIST
     from the app's embedded daemon plist, with BundleProgram replaced by the
     absolute path of the copy;
  3. create $LOG_DIR and "$STATE_DIR" (mode 0700);
  4. load the job into launchd as $DAEMON_LABEL, replacing it if already loaded,
     and wait until the daemon runs and its socket exists.
The daemon then runs as root and listens on $SOCKET_PATH. Its log is
$LOG_FILE (readable by root only).
PLAN
if [ "$DRY_RUN" = 1 ]; then
    echo "(dry run: nothing was changed)"
    exit 0
fi
[ "$(id -u)" -eq 0 ] || die "this changes system files: run it with sudo"
refuse_foreign_job "unregister it first (Uninstall Helper on the General page of the app, or System Settings > General > Login Items & Extensions)"
confirm "$ASSUME_YES"

# Stage next to the destination, then verify the copy rather than the source:
# the source may be writable by someone else.
STAGED="$DEV_APP_DIR.new"
install -d -m 0755 -o root -g wheel "$(dirname "$DEV_APP_DIR")"
rm -rf "$STAGED"
ditto "$APP" "$STAGED"
chown -R root:wheel "$STAGED"
chmod -R go-w "$STAGED"
codesign --verify --deep --strict "$STAGED" || die "the copy of $APP does not pass codesign verification"

PLIST_TMP="$(mktemp)"
trap 'rm -f "$PLIST_TMP"' EXIT
cp "$STAGED/Contents/Library/LaunchDaemons/$DAEMON_LABEL.plist" "$PLIST_TMP"
plutil -remove BundleProgram "$PLIST_TMP"
plutil -insert Program -string "$DEV_APP_DIR/Contents/MacOS/plaitwayd" "$PLIST_TMP"
plutil -lint "$PLIST_TMP" >/dev/null

if job_loaded; then
    log "stopping the loaded job"
    launchctl bootout "system/$DAEMON_LABEL"
    # bootout can return while the daemon is still removing its routes and DNS
    # entries; bootstrapping the new job over the old one would fail.
    wait_until $((DAEMON_EXIT_TIMEOUT + 5)) job_gone || die "$DAEMON_LABEL is still loaded after $((DAEMON_EXIT_TIMEOUT + 5)) s"
fi
rm -f "$SOCKET_PATH"
rm -rf "$DEV_APP_DIR"
mv "$STAGED" "$DEV_APP_DIR"
install -m 0644 -o root -g wheel "$PLIST_TMP" "$DEV_PLIST"
install -d -m 0755 -o root -g wheel "$LOG_DIR"
install -d -m 0700 -o root -g wheel "$STATE_DIR"

log "loading $DAEMON_LABEL"
launchctl bootstrap system "$DEV_PLIST"
if ! wait_until 20 helper_running; then
    printf 'error: the daemon is not running 20 s after it was loaded.\n' >&2
    if [ -f "$LOG_FILE" ]; then
        printf 'The end of %s:\n' "$LOG_FILE" >&2
        tail -n 20 "$LOG_FILE" >&2
    else
        printf 'It wrote no log to %s.\n' "$LOG_FILE" >&2
    fi
    die "the files are installed; fix the cause and run this script again, or undo it with scripts/dev-uninstall-daemon.sh"
fi
log "installed and running; log: sudo tail -f $LOG_FILE"
