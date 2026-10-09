#!/bin/sh
# Reverses scripts/linux/dev-install-daemon.sh: stops and disables the service,
# removes the unit and the copies of plaitwayd and plaitway. The profiles are
# kept unless --purge asks for them to go.
#
#   sudo scripts/linux/dev-uninstall-daemon.sh [--purge] [--yes] [--dry-run]
#
# Safe to run when nothing is installed. It refuses to touch a unit that the
# package (or make install) owns: remove that the way it was installed.
set -eu
HERE="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=../../packaging/linux/lib.sh
. "$HERE/../../packaging/linux/lib.sh"

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
  1. stop and disable $UNIT_NAME (the daemon removes its routes and DNS
     settings while stopping, which can take up to 30 seconds);
  2. remove ${DEV_UNIT#"$DEV_ROOT"}, ${DEV_DAEMON#"$DEV_ROOT"}
     and ${DEV_CLI#"$DEV_ROOT"};
  3. remove the run directory $RUN_DIR.
PLAN
if [ "$PURGE" = 1 ]; then
    cat <<PLAN
  4. DELETE $STATE_DIR: every stored profile, with the private keys and
     certificates inside, and the route journal.
PLAN
else
    cat <<PLAN
It keeps $STATE_DIR: the profiles hold private keys, and the daemon
cannot tell an uninstall from an upgrade. Run with --purge to delete it.
PLAN
fi
if [ "$DRY_RUN" = 1 ]; then
    echo "(dry run: nothing was changed)"
    exit 0
fi
[ -n "$DEV_ROOT" ] || [ "$(id -u)" -eq 0 ] || die "this changes system files: run it with sudo"
if reason="$(foreign_unit_reason)"; then
    die "$reason; remove it the way it was installed, then run this script again"
fi
confirm "$ASSUME_YES"

if [ -e "$DEV_UNIT" ]; then
    log "stopping $UNIT_NAME"
    systemctl disable --now "$UNIT_NAME"
fi
rm -f -- "$DEV_UNIT" "$DEV_DAEMON" "$DEV_CLI"
rmdir "$(dirname "$DEV_DAEMON")" 2>/dev/null || true
systemctl daemon-reload
# Only after the daemon is gone: it owns this directory while it runs.
rm -rf -- "$DEV_ROOT$RUN_DIR"
if [ "$PURGE" = 1 ]; then
    rm -rf -- "$DEV_ROOT$STATE_DIR"
    log "uninstalled; deleted $STATE_DIR"
else
    log "uninstalled; kept $STATE_DIR"
fi
