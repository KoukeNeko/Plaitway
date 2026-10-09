# shellcheck shell=sh
# shellcheck disable=SC2034 # the variables are used by the scripts that source this
# Shared helpers of the Linux packaging scripts. Source it from a script in
# packaging/linux or scripts/linux after setting HERE to the directory of that
# script; do not execute it. Plain POSIX sh: dash is /bin/sh on Debian and Ubuntu.

ROOT="$(cd "$HERE/../.." && pwd)"
PACKAGING_LINUX="$ROOT/packaging/linux"
# PLAITWAY_LINUX_BUILD moves every build product, for the tests of these scripts.
BUILD_DIR="${PLAITWAY_LINUX_BUILD:-$ROOT/build/linux}"

# The maintainer of the package and the signer of its changelog: the identity
# the repository's commits use.
MAINTAINER="${PLAITWAY_DEB_MAINTAINER:-KoukeNeko <111033412+KoukeNeko@users.noreply.github.com>}"

# Names shared by the install map, the package and the development scripts.
UNIT_NAME=plaitwayd.service
# plaitwayd is not a program for a person to run, so it is not on PATH.
LIBEXEC_NAME=plaitway
STATE_DIR=/var/lib/plaitway
RUN_DIR=/run/plaitway
SOCKET_PATH=$RUN_DIR/plaitwayd.sock

log() { printf '==> %s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

# read_version sets VERSION from the VERSION file.
read_version() {
    VERSION="$(tr -d '[:space:]' <"$ROOT/VERSION")"
    printf '%s\n' "$VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' || die "VERSION must look like 1.2.3, got '$VERSION'"
}

# write_changelog VERSION NOTES MAINTAINER EPOCH prints the changelog of the
# package in the format of Debian: one entry for VERSION, made of the notes file
# NOTES (Markdown; the entry says only "Release VERSION." without it), signed by
# MAINTAINER at the time EPOCH. Headings are dropped, each paragraph or list item
# becomes one entry, and the lines are wrapped to 76 columns.
write_changelog() {
    printf 'plaitway (%s) unstable; urgency=medium\n\n' "$1"
    if [ -f "$2" ]; then
        awk '
            function flush(   n, i, w, line) {
                if (entry == "") return
                n = split(entry, w, " ")
                line = "  *"
                for (i = 1; i <= n; i++) {
                    if (length(line) + 1 + length(w[i]) > 76) { print line; line = "   " }
                    line = line " " w[i]
                }
                print line
                entry = ""
            }
            /^#/ { flush(); next }
            /^[[:space:]]*$/ { flush(); next }
            /^- / { flush(); entry = substr($0, 3); next }
            { entry = (entry == "" ? $0 : entry " " $0) }
            END { flush() }
        ' "$2"
    else
        printf '  * Release %s.\n' "$1"
    fi
    printf '\n -- %s  %s\n' "$3" "$(date -R -u -d "@$4")"
}

# need_arg OPTION COUNT: the option needs a value after it.
need_arg() { [ "$2" -ge 2 ] || die "$1 needs a value"; }

# unit_stop_budget UNIT_FILE: TimeoutStopSec of the unit, in whole seconds.
unit_stop_budget() { sed -n 's/^TimeoutStopSec=\([0-9][0-9]*\)$/\1/p' "$1"; }

# daemon_stop_budget DAEMON_GO: serverStopTimeout plus shutdownTimeout in
# cmd/plaitwayd/daemon.go, in whole seconds: what the daemon needs to stop.
daemon_stop_budget() {
    total=0
    for name in serverStopTimeout shutdownTimeout; do
        seconds="$(sed -nE "s/^[[:space:]]*${name}[[:space:]]*=[[:space:]]*([0-9]+)[[:space:]]*\*[[:space:]]*time\.Second.*/\1/p" "$1")"
        [ -n "$seconds" ] || { echo "cannot read $name in $1" >&2; return 2; }
        total=$((total + seconds))
    done
    echo "$total"
}

# confirm ASSUME_YES asks on the terminal before a system change, unless
# ASSUME_YES is 1.
confirm() {
    [ "$1" = 1 ] && return 0
    { : </dev/tty; } 2>/dev/null || die "no terminal to ask on; pass --yes to proceed"
    printf 'Proceed? [y/N] ' >/dev/tty
    read -r answer </dev/tty
    case "$answer" in y | Y | yes) ;; *) die "aborted, nothing was changed" ;; esac
}

# Helpers of the scripts that install the daemon for development
# (scripts/linux/dev-install-daemon.sh and dev-uninstall-daemon.sh).

# PLAITWAY_DEV_ROOT puts every path below a directory, for the tests of those
# scripts, which run with fakes and without root. Unset in real use.
DEV_ROOT="${PLAITWAY_DEV_ROOT:-}"
# A root-owned copy in /usr/local: a root daemon must never run from a place the
# user can write. The unit goes to /etc/systemd/system, the administrator's
# place, which wins over /usr/lib/systemd/system.
DEV_DAEMON="$DEV_ROOT/usr/local/libexec/$LIBEXEC_NAME/plaitwayd"
DEV_CLI="$DEV_ROOT/usr/local/bin/plaitway"
DEV_UNIT="$DEV_ROOT/etc/systemd/system/$UNIT_NAME"
# The first line the dev-install script writes into its unit; the scripts touch
# no unit without it.
DEV_MARKER="# Installed by scripts/linux/dev-install-daemon.sh; scripts/linux/dev-uninstall-daemon.sh removes it."
# Where a package, or make install, puts the unit.
OTHER_UNIT_DIRS="/usr/lib/systemd/system /lib/systemd/system /usr/local/lib/systemd/system"

# Seconds between polls of wait_until; the tests of these helpers shorten it.
POLL_INTERVAL="${PLAITWAY_POLL_INTERVAL:-1}"

# wait_until SECONDS COMMAND...: runs COMMAND once per poll, for up to SECONDS
# seconds, until it succeeds.
wait_until() {
    wu_seconds=$1
    wu_waited=0
    shift
    until "$@"; do
        [ "$wu_waited" -lt "$wu_seconds" ] || return 1
        sleep "$POLL_INTERVAL"
        wu_waited=$((wu_waited + 1))
    done
}

# helper_running: the unit is active and its socket exists.
helper_running() {
    [ -S "$DEV_ROOT$SOCKET_PATH" ] && systemctl is-active --quiet "$UNIT_NAME"
}

# foreign_unit_reason prints why the unit is not the development install's, and
# succeeds, when the package or another install owns it; it fails, printing
# nothing, when the unit is absent or ours.
foreign_unit_reason() {
    if dpkg-query -W -f '${Status}' plaitway 2>/dev/null | grep -q 'install ok installed'; then
        echo "the plaitway package is installed; remove it first (apt remove plaitway)"
        return 0
    fi
    for fu_dir in $OTHER_UNIT_DIRS; do
        if [ -e "$DEV_ROOT$fu_dir/$UNIT_NAME" ]; then
            echo "$fu_dir/$UNIT_NAME exists, from a package or from make install"
            return 0
        fi
    done
    if [ -e "$DEV_UNIT" ] && [ "$(head -n 1 "$DEV_UNIT")" != "$DEV_MARKER" ]; then
        echo "${DEV_UNIT#"$DEV_ROOT"} was not written by dev-install-daemon.sh"
        return 0
    fi
    return 1
}

