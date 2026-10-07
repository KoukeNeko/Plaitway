#!/bin/bash
# Reverses scripts/dev-install-daemon.sh: unloads the job, removes the plist and
# the root-owned app copy, and cleans the run directory (sockets, generated
# configs). Profiles and logs are kept unless --purge asks for them to go.
#
#   sudo scripts/dev-uninstall-daemon.sh [--purge] [--yes] [--dry-run]
#
# Safe to run when nothing is installed. It also finishes an uninstall that was
# done from the app (Uninstall Helper): with the job gone, --purge removes the
# profiles and logs that the app leaves behind. Documented in packaging/README.md.
set -euo pipefail
# shellcheck source=../packaging/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/../packaging/lib.sh"

DRY_RUN=0
ASSUME_YES=0
PURGE=0
while [ $# -gt 0 ]; do
    case "$1" in
        --purge) PURGE=1; shift ;;
        --yes) ASSUME_YES=1; shift ;;
        --dry-run) DRY_RUN=1; shift ;;
        *) die "usage: sudo $0 [--purge] [--yes] [--dry-run]" ;;
    esac
done

cat <<PLAN
This will:
  1. unload $DAEMON_LABEL from launchd if it is loaded (the daemon removes its
     routes and DNS entries while stopping, which can take up to
     $DAEMON_EXIT_TIMEOUT seconds);
  2. remove $DEV_PLIST
     and $DEV_APP_DIR;
  3. remove the run directory $RUN_DIR.
PLAN
if [ "$PURGE" = 1 ]; then
    cat <<PLAN
  4. DELETE "$STATE_DIR": every stored profile, with the
     private keys and certificates inside, and the route journal;
  5. DELETE $LOG_DIR.
PLAN
else
    cat <<PLAN
It keeps "$STATE_DIR": the profiles hold private keys, and the
daemon cannot tell an uninstall from an upgrade. It keeps $LOG_DIR. Run with
--purge to delete both.
PLAN
fi
cat <<PLAN
It does not touch the login Keychain or the app's preferences, which belong to
your user, not to root; deleting a profile in the app removes its saved
credentials.
PLAN
if [ "$DRY_RUN" = 1 ]; then
    echo "(dry run: nothing was changed)"
    exit 0
fi
[ "$(id -u)" -eq 0 ] || die "this changes system files: run it with sudo"
refuse_foreign_job "uninstall it from the app (General, Uninstall Helper), then run this script again"
confirm "$ASSUME_YES"

if job_loaded; then
    log "unloading $DAEMON_LABEL"
    launchctl bootout "system/$DAEMON_LABEL"
    wait_until $((DAEMON_EXIT_TIMEOUT + 5)) job_gone ||
        die "$DAEMON_LABEL is still loaded after $((DAEMON_EXIT_TIMEOUT + 5)) s; nothing was removed"
fi
rm -f -- "$DEV_PLIST"
rm -rf -- "$DEV_APP_DIR" "$DEV_APP_DIR.new"
# Only after the daemon is gone: it owns this directory while it runs.
rm -rf -- "$RUN_DIR"
if [ "$PURGE" = 1 ]; then
    rm -rf -- "$STATE_DIR" "$LOG_DIR"
    log "uninstalled; deleted \"$STATE_DIR\" and $LOG_DIR"
else
    log "uninstalled; kept \"$STATE_DIR\" and $LOG_DIR"
fi
