#!/bin/sh
# Installs the plaitwayd and plaitway you built (make linux-build) as the
# system service, without the package: root-owned copies in /usr/local, and the
# unit in /etc/systemd/system, started and enabled.
#
#   sudo scripts/linux/dev-install-daemon.sh [--build DIR] [--yes] [--dry-run]
#
# Safe to run again: it replaces the copies and restarts the service. Undo it
# with scripts/linux/dev-uninstall-daemon.sh. It refuses to touch a unit that
# the package (or make install) owns.
set -eu
HERE="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=../../packaging/linux/lib.sh
. "$HERE/../../packaging/linux/lib.sh"

DRY_RUN=0
ASSUME_YES=0
while [ $# -gt 0 ]; do
    case "$1" in
        --build) need_arg "$1" $#; BUILD_DIR="$2"; shift 2 ;;
        --yes) ASSUME_YES=1; shift ;;
        --dry-run) DRY_RUN=1; shift ;;
        *) die "usage: sudo $0 [--build DIR] [--yes] [--dry-run]" ;;
    esac
done

for program in plaitwayd plaitway; do
    [ -x "$BUILD_DIR/bin/$program" ] || die "$BUILD_DIR/bin/$program is missing; build it with make linux-build"
done

cat <<PLAN
This will:
  1. copy $BUILD_DIR/bin/plaitwayd
     to ${DEV_DAEMON#"$DEV_ROOT"} and $BUILD_DIR/bin/plaitway
     to ${DEV_CLI#"$DEV_ROOT"}, owned by root and not writable by others;
  2. write ${DEV_UNIT#"$DEV_ROOT"} from packaging/linux/$UNIT_NAME,
     with the path of the daemon replaced;
  3. enable the service and (re)start it, then wait until it runs and its
     socket exists.
The daemon then runs as root and listens on $SOCKET_PATH. Its log is
in the journal: journalctl -u $UNIT_NAME
PLAN
if [ "$DRY_RUN" = 1 ]; then
    echo "(dry run: nothing was changed)"
    exit 0
fi
[ -n "$DEV_ROOT" ] || [ "$(id -u)" -eq 0 ] || die "this changes system files: run it with sudo"
if reason="$(foreign_unit_reason)"; then
    die "$reason; this script installs its own unit, and would replace it"
fi
confirm "$ASSUME_YES"

# The running daemon removes its routes and DNS settings while it stops, which
# the unit allows 30 s for; the files are replaced only after that.
if systemctl is-active --quiet "$UNIT_NAME"; then
    log "stopping $UNIT_NAME"
    systemctl stop "$UNIT_NAME"
fi

install -d -m 0755 -o root -g root "$(dirname "$DEV_DAEMON")" "$(dirname "$DEV_CLI")" "$(dirname "$DEV_UNIT")"
# Written next to the destination and moved into place, so that no one sees a
# half-written program.
for pair in "plaitwayd:$DEV_DAEMON" "plaitway:$DEV_CLI"; do
    install -m 0755 -o root -g root "$BUILD_DIR/bin/${pair%%:*}" "${pair#*:}.new"
    mv -f "${pair#*:}.new" "${pair#*:}"
done
{
    printf '%s\n' "$DEV_MARKER"
    sed "s|^ExecStart=/usr/libexec/|ExecStart=/usr/local/libexec/|" "$PACKAGING_LINUX/$UNIT_NAME"
} >"$DEV_UNIT.new"
chmod 0644 "$DEV_UNIT.new"
mv -f "$DEV_UNIT.new" "$DEV_UNIT"

log "starting $UNIT_NAME"
systemctl daemon-reload
systemctl enable "$UNIT_NAME"
systemctl restart "$UNIT_NAME" || true
if ! wait_until 20 helper_running; then
    printf 'error: the service is not running 20 s after it was started.\n' >&2
    journalctl -u "$UNIT_NAME" -n 20 --no-pager >&2 || true
    die "the files are installed; fix the cause and run this script again, or undo it with scripts/linux/dev-uninstall-daemon.sh"
fi
log "installed and running; log: journalctl -fu $UNIT_NAME"
