#!/bin/sh
# Removes Plaitway from this computer completely: the app, its GNOME Shell
# extension, the passwords it saved in the keyring, its files in your home
# directory, the package with the helper and every stored profile, and the apt
# repository. Other people's keyrings and home directories are theirs to clean.
#
#   packaging/linux/uninstall.sh [--yes] [--dry-run]
#   curl -fsSL https://raw.githubusercontent.com/KoukeNeko/Plaitway/main/packaging/linux/uninstall.sh | sh
#
# Run it as yourself, not with sudo: the keyring and the home directory are
# yours, and it asks sudo for the steps that need root. Safe to run again, or
# where only part of Plaitway is installed. A helper installed by
# scripts/linux/dev-install-daemon.sh or `make install` is removed as
# Docs/uninstall.md says.
#
# Self-contained, so that it runs without a checkout.
set -eu

# For the tests: PLAITWAY_DEV_ROOT puts the system paths below a directory, and
# PLAITWAY_PYTHON is the Python that clears the keyring. Unset in real use.
DEV_ROOT="${PLAITWAY_DEV_ROOT:-}"
# The system's Python, which has the GObject bindings the app uses; the first on
# PATH may be another one (Homebrew, a virtual environment).
PYTHON="${PLAITWAY_PYTHON:-/usr/bin/python3}"

PACKAGE=plaitway
UUID=plaitway@koukeneko.github.io
APP_ID=io.github.koukeneko.Plaitway
SECRET_SCHEMA=io.github.koukeneko.plaitway.credentials
STATE_DIR=/var/lib/plaitway
APT_LIST=/etc/apt/sources.list.d/plaitway.list
APT_KEY=/etc/apt/keyrings/plaitway.asc

EXTENSION_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/gnome-shell/extensions/$UUID"
APP_STATE_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/plaitway"
AUTOSTART="${XDG_CONFIG_HOME:-$HOME/.config}/autostart/$APP_ID.desktop"

log() { printf '==> %s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

ASSUME_YES=0
DRY_RUN=0
while [ $# -gt 0 ]; do
    case "$1" in
        --yes) ASSUME_YES=1; shift ;;
        --dry-run) DRY_RUN=1; shift ;;
        *) die "usage: $0 [--yes] [--dry-run]" ;;
    esac
done

cat <<PLAN
This will remove Plaitway from this computer:
  1. quit the app;
  2. turn off the GNOME Shell extension and delete the copy in your home
     ($EXTENSION_DIR);
  3. delete the passwords Plaitway saved in your keyring;
  4. delete $APP_STATE_DIR and the login item $AUTOSTART;
  5. purge the $PACKAGE package: the helper stops, removing its routes and DNS
     settings, and $STATE_DIR is DELETED with every stored profile and the
     private keys and certificates inside, which cannot be made again;
  6. remove the apt repository ($APT_LIST, $APT_KEY).
PLAN
if [ "$DRY_RUN" = 1 ]; then
    echo "(dry run: nothing was changed)"
    exit 0
fi
[ "$(id -u)" -ne 0 ] || die "run it as yourself, not with sudo: your keyring and home directory are cleaned too"
if [ "$ASSUME_YES" != 1 ]; then
    # true, not the special builtin `:`, whose failed redirection would end the shell.
    { true </dev/tty; } 2>/dev/null || die "no terminal to ask on; pass --yes to proceed"
    printf 'Proceed? [y/N] ' >/dev/tty
    read -r answer </dev/tty
    case "$answer" in y | Y | yes) ;; *) die "aborted, nothing was changed" ;; esac
fi

# SIGTERM ends the app at once, without asking about the connected profiles; the
# helper disconnects them when the package stops it.
if pkill -TERM -u "$(id -u)" -f '/plaitway-app( |$)'; then
    log "quit the app"
fi

# The extension is turned off as the app's Tools page does it, and the saved
# passwords are found by the schema the app stores them under. A keyring that
# cannot be reached is reported, and the rest goes on.
if [ -x "$PYTHON" ]; then
    "$PYTHON" - "$UUID" "$SECRET_SCHEMA" <<'PY' || printf 'warning: the extension or the keyring could not be cleaned (above); the rest goes on\n' >&2
import sys

import gi

gi.require_version("Gio", "2.0")
from gi.repository import Gio, GLib

uuid, schema_name = sys.argv[1:]
status = 0

source = Gio.SettingsSchemaSource.get_default()
if source is not None and source.lookup("org.gnome.shell", True) is not None:
    settings = Gio.Settings.new("org.gnome.shell")
    for key in ("enabled-extensions", "disabled-extensions"):
        names = settings.get_strv(key)
        if uuid in names:
            settings.set_strv(key, [name for name in names if name != uuid])
    Gio.Settings.sync()
    print("==> turned off the GNOME Shell extension")

try:
    gi.require_version("Secret", "1")
    from gi.repository import Secret

    schema = Secret.Schema.new(schema_name, Secret.SchemaFlags.NONE, {})
    if Secret.password_clear_sync(schema, {}, None):
        print("==> deleted the saved passwords from the keyring")
except (ValueError, GLib.Error) as error:
    print(f"error: the keyring: {error}", file=sys.stderr)
    status = 1
sys.exit(status)
PY
else
    printf 'warning: %s is missing: the extension stays turned on and the saved passwords stay in the keyring\n' "$PYTHON" >&2
fi

rm -rf -- "$EXTENSION_DIR" "$APP_STATE_DIR"
rm -f -- "$AUTOSTART"
log "deleted Plaitway's files in your home directory"

# config-files: removed earlier but not purged, so the profiles are still there.
case "$(dpkg-query -W -f='${db:Status-Status}' "$PACKAGE" 2>/dev/null || true)" in
    installed | config-files | half-installed | half-configured | unpacked | triggers-awaited | triggers-pending)
        log "purging the $PACKAGE package (sudo)"
        sudo apt-get purge -y "$PACKAGE"
        ;;
esac
if [ -e "$DEV_ROOT$STATE_DIR" ]; then
    printf 'warning: %s is still there: a helper not installed by the package uses it; see Docs/uninstall.md\n' "$STATE_DIR" >&2
fi

if [ -e "$DEV_ROOT$APT_LIST" ] || [ -e "$DEV_ROOT$APT_KEY" ]; then
    log "removing the apt repository (sudo)"
    sudo rm -f -- "$DEV_ROOT$APT_LIST" "$DEV_ROOT$APT_KEY"
    sudo apt-get update
fi

log "Plaitway is removed; log out and back in to take the extension out of the top bar"
