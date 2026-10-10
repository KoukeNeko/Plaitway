#!/bin/sh
# Tests of the Linux packaging and install scripts that need no root, no build
# and no systemd: install.sh on stand-in binaries, the maintainer scripts of the
# package and the development install scripts against fakes (systemctl,
# install, dpkg-query, deb-systemd-helper), and the unit's stop timeout against
# the daemon's shutdown budget. `make test-packaging-linux` runs it.
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
FAILURES=0
OUTPUT=""
STATUS=0

pass() { printf 'ok    %s\n' "$1"; }
fail() { printf 'FAIL  %s\n' "$1"; FAILURES=$((FAILURES + 1)); }
# expect DESCRIPTION CONDITION: passes when the shell code CONDITION succeeds.
expect() {
    if eval "$2"; then
        pass "$1"
    else
        fail "$1 (status $STATUS, output: $OUTPUT)"
    fi
}
output_has() { printf '%s\n' "$OUTPUT" | grep -q -e "$1"; }
calls_have() { grep -q -e "$1" "$TMP/calls"; }
calls_lack() { ! calls_have "$1"; }
# line_before FIRST SECOND: FIRST is in the call log, and before SECOND.
line_before() {
    a="$(grep -n -e "$1" "$TMP/calls" | head -n 1 | cut -d: -f1)"
    b="$(grep -n -e "$2" "$TMP/calls" | head -n 1 | cut -d: -f1)"
    [ -n "$a" ] && [ -n "$b" ] && [ "$a" -lt "$b" ]
}
mode_of() { stat -c %a "$1"; }
# run COMMAND...: runs it, keeping its output and status.
run() {
    OUTPUT="$("$@" 2>&1)"
    STATUS=$?
}

# --- the unit against the daemon's shutdown budget -----------------------------

# The helpers run in a subshell: lib.sh sets variables of its own.
stop_budgets() {
    (
        # shellcheck source=lib.sh
        . "$HERE/lib.sh"
        unit="$(unit_stop_budget "$HERE/plaitwayd.service")"
        daemon="$(daemon_stop_budget "$ROOT/cmd/plaitwayd/daemon.go")"
        [ -n "$unit" ] && [ -n "$daemon" ] && [ "$daemon" -lt "$unit" ]
    )
}
run stop_budgets
expect "TimeoutStopSec of the unit is above the daemon's shutdown budget" 'test "$STATUS" -eq 0'

budget() {
    OUTPUT="$(. "$HERE/lib.sh"; daemon_stop_budget "$1" 2>&1)"
    STATUS=$?
}
printf 'const (\n\tserverStopTimeout = 2 * time.Second\n\tshutdownTimeout = 15 * time.Second\n)\n' >"$TMP/daemon.go"
budget "$TMP/daemon.go"
expect "the budget is the sum of the two timeouts" 'test "$STATUS" -eq 0 -a "$OUTPUT" = 17'
printf 'const shutdownTimeout = time.Minute\n' >"$TMP/odd.go"
budget "$TMP/odd.go"
expect "a budget that cannot be read is an error" 'test "$STATUS" -ne 0'

# --- the changelog -------------------------------------------------------------

NOTES="$TMP/notes.md"
cat >"$NOTES" <<'NOTES'
## Changes

- The status card on a profile's Overview is redesigned: the shield sits on a tile of the state's colour, level with the state word, and with a cause to show the tile and the words share the top
- A tunnel with no traffic reads "0 bytes/s"

The helper is the same as in 0.3.3, which is
written over two lines.
NOTES
changelog() { # NOTES
    OUTPUT="$(. "$HERE/lib.sh"; write_changelog 1.2.3 "$1" "A <a@example.org>" 86400 2>&1)"
    STATUS=$?
}
changelog "$NOTES"
expect "the changelog starts with the entry of the version" 'printf "%s\n" "$OUTPUT" | head -n 1 | grep -qx "plaitway (1.2.3) unstable; urgency=medium"'
expect "the changelog has one entry for each list item and paragraph, and none for the heading" \
    'test "$(printf "%s\n" "$OUTPUT" | grep -c "^  \* ")" -eq 3 && ! printf "%s\n" "$OUTPUT" | grep -q Changes'
expect "the changelog wraps its lines to 76 columns, with a four-space continuation" \
    'test -z "$(printf "%s\n" "$OUTPUT" | awk "length > 76")" && printf "%s\n" "$OUTPUT" | grep -q "^    on a tile of the state"'
expect "a paragraph over two lines is one entry" 'printf "%s\n" "$OUTPUT" | grep -q "^  \* The helper is the same as in 0.3.3, which is written over two lines.$"'
expect "the changelog is signed with the maintainer and the date" 'printf "%s\n" "$OUTPUT" | tail -n 1 | grep -qx " -- A <a@example.org>  Fri, 02 Jan 1970 00:00:00 +0000"'
if command -v dpkg-parsechangelog >/dev/null 2>&1; then
    expect "dpkg-parsechangelog reads it" '[ "$(printf "%s\n" "$OUTPUT" | dpkg-parsechangelog -l- --show-field Version)" = 1.2.3 ]'
fi
changelog "$TMP/no-such-notes.md"
expect "without notes the entry says only that it is a release" 'printf "%s\n" "$OUTPUT" | grep -qx "  \* Release 1.2.3."'

# --- install.sh --------------------------------------------------------------

BUILD="$TMP/build"
mkdir -p "$BUILD/bin"
printf '#!/bin/sh\necho daemon\n' >"$BUILD/bin/plaitwayd"
printf '#!/bin/sh\necho cli\n' >"$BUILD/bin/plaitway"
printf '# notices\n' >"$BUILD/THIRD_PARTY_NOTICES.md"
printf 'plaitway (1.2.3) unstable; urgency=medium\n\n  * test\n\n -- A <a@example.org>  Thu, 01 Jan 1970 00:00:00 +0000\n' >"$BUILD/changelog"

# install_into DESTDIR ARGS...: install.sh with a restrictive umask, on the stand-ins.
install_into() {
    dest=$1
    shift
    (umask 077; sh "$HERE/install.sh" --destdir "$dest" --build "$BUILD" --version 1.2.3 "$@")
}

run install_into "$TMP/usr" --prefix /usr
expect "install.sh with --prefix /usr succeeds" 'test "$STATUS" -eq 0'
# shellcheck disable=SC2034 # read by the commands that check and expect evaluate
D="$TMP/usr/usr"
expect "the daemon is in libexec and not on PATH" 'test -x "$D/libexec/plaitway/plaitwayd" -a ! -e "$D/bin/plaitwayd"'
expect "the client and the app launcher are in bin" 'test -x "$D/bin/plaitway" -a -x "$D/bin/plaitway-app"'
expect "the unit is in lib/systemd/system with the package path of the daemon" \
    'grep -q "^ExecStart=/usr/libexec/plaitway/plaitwayd " "$D/lib/systemd/system/plaitwayd.service"'
expect "the Python package is in dist-packages, with its descriptor" \
    'test -f "$D/lib/python3/dist-packages/plaitway/app/main.py" -a -f "$D/lib/python3/dist-packages/plaitway/client/descriptor.binpb"'
expect "no bytecode cache is installed" 'test -z "$(find "$D" -name __pycache__ -o -name "*.pyc")"'
expect "the installed version.py has the release version" 'grep -qx "__version__ = \"1.2.3\"" "$D/lib/python3/dist-packages/plaitway/version.py"'
expect "the version in the source tree is not touched" 'grep -qx "__version__ = \"0.0.0-dev\"" "$ROOT/linux/src/plaitway/version.py"'
expect "the share files are in share" \
    'test -f "$D/share/applications/io.github.koukeneko.Plaitway.desktop" -a -f "$D/share/metainfo/io.github.koukeneko.Plaitway.metainfo.xml" -a -f "$D/share/mime/packages/io.github.koukeneko.Plaitway.xml" -a -f "$D/share/icons/hicolor/48x48/apps/io.github.koukeneko.Plaitway.png"'
expect "the documents are in share/doc/plaitway" \
    'test -f "$D/share/doc/plaitway/copyright" -a -f "$D/share/doc/plaitway/THIRD_PARTY_NOTICES.md" -a -f "$D/share/doc/plaitway/changelog.gz"'
expect "the changelog is gzip without a time stamp, and is the changelog" \
    'test "$(od -An -tx1 -j4 -N4 "$D/share/doc/plaitway/changelog.gz" | tr -d " ")" = 00000000 && gzip -dc "$D/share/doc/plaitway/changelog.gz" | cmp -s - "$BUILD/changelog"'
expect "programs are 0755 and data 0644, whatever the umask" \
    'test "$(mode_of "$D/bin/plaitway")" = 755 -a "$(mode_of "$D/lib/systemd/system/plaitwayd.service")" = 644 -a "$(mode_of "$D/lib/python3/dist-packages/plaitway/version.py")" = 644'
expect "every directory is 0755, whatever the umask" 'test -z "$(find "$TMP/usr" -type d ! -perm 755)"'
expect "nothing is writable by group or others" 'test -z "$(find "$TMP/usr" ! -type l -perm /022)"'

run install_into "$TMP/usr" --prefix /usr
expect "a second install over the first succeeds" 'test "$STATUS" -eq 0'

run install_into "$TMP/local" --prefix /opt/plaitway --python-dir /opt/plaitway/lib/python3/site-packages
expect "install.sh with another prefix succeeds" 'test "$STATUS" -eq 0'
expect "the unit names the daemon below the prefix" \
    'grep -q "^ExecStart=/opt/plaitway/libexec/plaitway/plaitwayd " "$TMP/local/opt/plaitway/lib/systemd/system/plaitwayd.service"'
expect "--python-dir decides where the package goes" 'test -f "$TMP/local/opt/plaitway/lib/python3/site-packages/plaitway/version.py"'
expect "the rest of the unit is as in the repository" \
    'sed "s|/opt/plaitway/libexec|/usr/libexec|" "$TMP/local/opt/plaitway/lib/systemd/system/plaitwayd.service" | cmp -s - "$HERE/plaitwayd.service"'

run install_into "$TMP/none" --prefix /nowhere/at/all
expect "a prefix below which Python looks nowhere is refused, naming --python-dir" 'test "$STATUS" -ne 0 && output_has -- "--python-dir"'
run install_into "$TMP/relative" --prefix usr
expect "a relative prefix is refused" 'test "$STATUS" -ne 0'

mkdir "$TMP/empty-build"
run sh "$HERE/install.sh" --destdir "$TMP/e" --build "$TMP/empty-build" --prefix /usr --version 1.2.3
expect "a build directory without the programs is refused, naming build.sh" 'test "$STATUS" -ne 0 && output_has "build.sh"'

# --- the OpenRC script ----------------------------------------------------------

OPENRC="$HERE/openrc/plaitwayd"
unit_args() { sed -n 's|^ExecStart=/usr/libexec/plaitway/plaitwayd ||p' "$HERE/plaitwayd.service"; }
openrc_args() { sed -n 's|^command_args="\(.*\)"$|\1|p' "$OPENRC"; }
# The helpers run in a subshell: lib.sh sets variables of its own.
openrc_stop_budget() (
    # shellcheck source=lib.sh
    . "$HERE/lib.sh"
    retry="$(sed -n 's|^retry="TERM/\([0-9]*\)/KILL/[0-9]*"$|\1|p' "$OPENRC")"
    daemon="$(daemon_stop_budget "$ROOT/cmd/plaitwayd/daemon.go")"
    [ -n "$retry" ] && [ -n "$daemon" ] && [ "$daemon" -lt "$retry" ]
)
expect "the OpenRC script is a valid shell script" 'sh -n "$OPENRC"'
expect "the OpenRC script starts the daemon with the arguments of the unit" '[ -n "$(unit_args)" ] && [ "$(unit_args)" = "$(openrc_args)" ]'
expect "the OpenRC script restarts the daemon without limit, after the unit's delay" \
    '[ "$(sed -n "s/^respawn_max=//p" "$OPENRC")" = 0 ] && [ -z "$(sed -n "s/^respawn_period=//p" "$OPENRC")" ] && [ "$(sed -n "s/^respawn_delay=//p" "$OPENRC")" = "$(sed -n "s/^RestartSec=//p" "$HERE/plaitwayd.service")" ]'
run openrc_stop_budget
expect "the OpenRC script waits for the daemon's shutdown budget before it kills" 'test "$STATUS" -eq 0'
expect "the OpenRC script deletes only names of Plaitway from resolvconf when it stops" \
    'grep -q "resolvconf -i .plaitway:\*." "$OPENRC" && grep -q "^[[:space:]]*plaitway:\*) resolvconf -f -d" "$OPENRC"'

run install_into "$TMP/openrc" --prefix /usr --openrc
expect "install.sh --openrc succeeds" 'test "$STATUS" -eq 0'
expect "the OpenRC script is in /etc/init.d below DESTDIR, mode 0755" \
    'test -x "$TMP/openrc/etc/init.d/plaitwayd" -a "$(mode_of "$TMP/openrc/etc/init.d/plaitwayd")" = 755 -a ! -e "$TMP/openrc/usr/etc"'
expect "it names the daemon by the package path, and is the script of the repository" 'cmp -s "$TMP/openrc/etc/init.d/plaitwayd" "$OPENRC"'
run install_into "$TMP/openrc-local" --prefix /opt/plaitway --python-dir /opt/plaitway/lib/python3/site-packages --openrc
expect "with another prefix it names the daemon below the prefix and changes nothing else" \
    'grep -qx "command=/opt/plaitway/libexec/plaitway/plaitwayd" "$TMP/openrc-local/etc/init.d/plaitwayd" && sed "s|/opt/plaitway/libexec|/usr/libexec|" "$TMP/openrc-local/etc/init.d/plaitwayd" | cmp -s - "$OPENRC"'
expect "without --openrc nothing goes to /etc" 'test ! -e "$TMP/usr/etc" -a ! -e "$TMP/local/etc"'

# --- the maintainer scripts, against fakes -----------------------------------

# The scripts call the tools by name and look for them by absolute path: the
# fakes are first on PATH, and the paths are rewritten to them. The real tools
# must never run here.
FAKE="$TMP/fake"
mkdir -p "$FAKE/bin" "$FAKE/sys"
cat >"$FAKE/bin/deb-systemd-helper" <<'FAKETOOL'
#!/bin/sh
echo "$(basename "$0") $*" >>"$FAKE_CALLS"
case "$*" in *was-enabled*) exit "${FAKE_WAS_ENABLED:-0}" ;; esac
exit "${FAKE_TOOL_STATUS:-0}"
FAKETOOL
for tool in deb-systemd-invoke systemctl py3compile py3clean; do
    cat >"$FAKE/bin/$tool" <<'FAKETOOL'
#!/bin/sh
echo "$(basename "$0") $*" >>"$FAKE_CALLS"
exit "${FAKE_TOOL_STATUS:-0}"
FAKETOOL
done
chmod +x "$FAKE"/bin/*
export FAKE_CALLS="$TMP/calls"

# maint SCRIPT ARGS...: a maintainer script with the absolute paths of the
# fakes, run with the fake tools first on PATH.
maint() {
    script=$1
    shift
    sed -e "s|/usr/bin/deb-systemd-|$FAKE/bin/deb-systemd-|g" -e "s|/run/systemd/system|$FAKE/sys|g" \
        -e "s|/var/lib/plaitway|$FAKE/state|g" -e "s|/run/plaitway|$FAKE/run|g" "$HERE/debian/$script" >"$FAKE/$script"
    : >"$TMP/calls"
    OUTPUT="$(PATH="$FAKE/bin:$PATH" sh "$FAKE/$script" "$@" 2>&1)"
    STATUS=$?
}

maint postinst configure
expect "postinst, first install: enables and starts the unit" \
    'test "$STATUS" -eq 0 && calls_have "deb-systemd-helper enable plaitwayd.service" && calls_have "deb-systemd-invoke start plaitwayd.service"'
expect "postinst, first install: reloads systemd before the start" 'line_before "systemctl --system daemon-reload" "deb-systemd-invoke start"'
expect "postinst, first install: compiles the app and does not restart" 'calls_have "py3compile -p plaitway" && calls_lack "invoke restart"'
maint postinst configure 0.3.3
expect "postinst, upgrade: restarts the unit" 'test "$STATUS" -eq 0 && calls_have "deb-systemd-invoke restart plaitwayd.service"'
export FAKE_WAS_ENABLED=1
maint postinst configure 0.3.3
unset FAKE_WAS_ENABLED
expect "postinst, disabled by the administrator: stays disabled" 'calls_have "update-state plaitwayd.service" && calls_lack "helper enable"'
rmdir "$FAKE/sys"
maint postinst configure
expect "postinst without systemd running: enables, starts nothing, succeeds" \
    'test "$STATUS" -eq 0 && calls_have "helper enable" && calls_lack "systemctl" && calls_lack "invoke"'
export FAKE_TOOL_STATUS=1
maint postinst configure
unset FAKE_TOOL_STATUS
expect "postinst: a failing systemd tool does not fail the installation" 'test "$STATUS" -eq 0'
mkdir "$FAKE/sys"

maint prerm remove
expect "prerm remove: stops the unit" 'test "$STATUS" -eq 0 && calls_have "deb-systemd-invoke stop plaitwayd.service" && calls_have "py3clean -p plaitway"'
maint prerm upgrade 0.3.5
expect "prerm upgrade: leaves the unit running" 'test "$STATUS" -eq 0 && calls_lack "invoke stop"'

mkdir -p "$FAKE/state/profiles" "$FAKE/run"
maint postrm remove
expect "postrm remove: masks the unit and keeps the profiles" \
    'test "$STATUS" -eq 0 && calls_have "deb-systemd-helper mask plaitwayd.service" && test -d "$FAKE/state/profiles"'
maint postrm upgrade 0.3.5
expect "postrm upgrade: keeps the profiles" 'test "$STATUS" -eq 0 && test -d "$FAKE/state/profiles"'
maint postrm purge
expect "postrm purge: deletes the profiles and the run directory" 'test "$STATUS" -eq 0 && test ! -e "$FAKE/state" -a ! -e "$FAKE/run"'
expect "postrm purge: forgets the enablement state and reloads systemd" \
    'calls_have "deb-systemd-helper purge plaitwayd.service" && calls_have "deb-systemd-helper unmask plaitwayd.service" && calls_have "systemctl --system daemon-reload"'
for script in postinst prerm postrm; do
    run dash -n "$HERE/debian/$script"
    expect "$script is valid POSIX sh" 'test "$STATUS" -eq 0'
done

# --- scripts/linux/dev-*-daemon.sh, against fakes ------------------------------

DEVBIN="$TMP/devbin"
DEVROOT="$TMP/devroot"
mkdir -p "$DEVBIN"
# systemctl keeps a state file: "active" after restart, gone after stop. restart
# makes the socket, the way the daemon does.
cat >"$DEVBIN/systemctl" <<'FAKETOOL'
#!/bin/sh
echo "systemctl $*" >>"$FAKE_CALLS"
state="$PLAITWAY_DEV_ROOT/.active"
case "$1" in
    is-active) [ -f "$state" ] ;;
    restart)
        [ "${FAKE_NEVER_RUNS:-0}" = 1 ] && exit 1
        : >"$state"
        mkdir -p "$PLAITWAY_DEV_ROOT/run/plaitway"
        python3 -c 'import socket, sys; socket.socket(socket.AF_UNIX).bind(sys.argv[1])' "$PLAITWAY_DEV_ROOT/run/plaitway/plaitwayd.sock" ;;
    stop | disable) rm -f "$state" "$PLAITWAY_DEV_ROOT/run/plaitway/plaitwayd.sock" ;;
esac
exit 0
FAKETOOL
# install without the ownership, which only root may give.
cat >"$DEVBIN/install" <<'FAKETOOL'
#!/bin/sh
args=""
while [ $# -gt 0 ]; do
    case "$1" in -o | -g) shift 2 ;; *) args="$args $1"; shift ;; esac
done
# shellcheck disable=SC2086
exec /usr/bin/install $args
FAKETOOL
cat >"$DEVBIN/dpkg-query" <<'FAKETOOL'
#!/bin/sh
[ "${FAKE_PACKAGE_INSTALLED:-0}" = 1 ] && printf 'install ok installed'
[ "${FAKE_PACKAGE_INSTALLED:-0}" = 1 ]
FAKETOOL
printf '#!/bin/sh\necho "journalctl $*" >>"$FAKE_CALLS"\necho journal-line\n' >"$DEVBIN/journalctl"
chmod +x "$DEVBIN"/*

chmod +x "$BUILD/bin/plaitwayd" "$BUILD/bin/plaitway"
mkdir -p "$TMP/devbuild/bin"
cp "$BUILD/bin/plaitwayd" "$BUILD/bin/plaitway" "$TMP/devbuild/bin/"

# dev SCRIPT ARGS...: a script of scripts/linux against the fakes and DEVROOT.
dev() {
    script=$1
    shift
    : >"$TMP/calls"
    OUTPUT="$(PLAITWAY_DEV_ROOT="$DEVROOT" PLAITWAY_POLL_INTERVAL=0 PATH="$DEVBIN:$PATH" \
        sh "$ROOT/scripts/linux/$script" --yes "$@" 2>&1)"
    STATUS=$?
}
# shellcheck disable=SC2034 # read by the commands that check and expect evaluate
UNIT_MARKER="# Installed by scripts/linux/dev-install-daemon.sh; scripts/linux/dev-uninstall-daemon.sh removes it."

dev dev-install-daemon.sh --build "$TMP/devbuild" --dry-run
expect "dev-install --dry-run changes nothing" 'test "$STATUS" -eq 0 -a ! -e "$DEVROOT" && output_has "nothing was changed"'
dev dev-install-daemon.sh --build "$TMP/empty-build"
expect "dev-install without built programs says how to build them" 'test "$STATUS" -ne 0 && output_has "make linux-build"'

dev dev-install-daemon.sh --build "$TMP/devbuild"
expect "dev-install succeeds" 'test "$STATUS" -eq 0'
expect "the daemon and the client are in /usr/local" 'test -x "$DEVROOT/usr/local/libexec/plaitway/plaitwayd" -a -x "$DEVROOT/usr/local/bin/plaitway"'
expect "the unit is in /etc/systemd/system, marked, and points at the copy" \
    'test "$(head -n 1 "$DEVROOT/etc/systemd/system/plaitwayd.service")" = "$UNIT_MARKER" && grep -q "^ExecStart=/usr/local/libexec/plaitway/plaitwayd " "$DEVROOT/etc/systemd/system/plaitwayd.service"'
expect "the unit is reloaded, enabled and started, in that order" \
    'line_before "daemon-reload" "systemctl enable plaitwayd.service" && line_before "systemctl enable" "systemctl restart"'
expect "no temporary file is left behind" 'test -z "$(find "$DEVROOT" -name "*.new")"'

dev dev-install-daemon.sh --build "$TMP/devbuild"
expect "dev-install again stops the running unit first" 'test "$STATUS" -eq 0 && line_before "systemctl stop plaitwayd.service" "systemctl restart"'

export FAKE_NEVER_RUNS=1
dev dev-install-daemon.sh --build "$TMP/devbuild"
unset FAKE_NEVER_RUNS
expect "a service that does not run is reported with its journal" 'test "$STATUS" -ne 0 && output_has "not running" && calls_have "journalctl -u plaitwayd.service"'

rm -rf "$DEVROOT"
export FAKE_PACKAGE_INSTALLED=1
dev dev-install-daemon.sh --build "$TMP/devbuild"
unset FAKE_PACKAGE_INSTALLED
expect "dev-install refuses while the package is installed" 'test "$STATUS" -ne 0 -a ! -e "$DEVROOT/usr/local" && output_has "package is installed"'
mkdir -p "$DEVROOT/usr/lib/systemd/system"
: >"$DEVROOT/usr/lib/systemd/system/plaitwayd.service"
dev dev-install-daemon.sh --build "$TMP/devbuild"
expect "dev-install refuses a unit from a package or make install" \
    'test "$STATUS" -ne 0 -a ! -e "$DEVROOT/usr/local" && output_has "/usr/lib/systemd/system/plaitwayd.service exists"'
rm -rf "$DEVROOT"
mkdir -p "$DEVROOT/etc/systemd/system"
printf '[Service]\nExecStart=/bin/true\n' >"$DEVROOT/etc/systemd/system/plaitwayd.service"
dev dev-install-daemon.sh --build "$TMP/devbuild"
expect "dev-install refuses to overwrite a unit it did not write" \
    'test "$STATUS" -ne 0 && output_has "was not written" && grep -q "/bin/true" "$DEVROOT/etc/systemd/system/plaitwayd.service"'
dev dev-uninstall-daemon.sh
expect "dev-uninstall refuses a unit it did not write, too" 'test "$STATUS" -ne 0 && grep -q "/bin/true" "$DEVROOT/etc/systemd/system/plaitwayd.service"'

# Never with root: without a test root this would change the machine.
if [ "$(id -u)" -ne 0 ]; then
    OUTPUT="$(PATH="$DEVBIN:$PATH" sh "$ROOT/scripts/linux/dev-install-daemon.sh" --yes --build "$TMP/devbuild" 2>&1)"
    STATUS=$?
    expect "without root and without a test root, dev-install stops before changing anything" 'test "$STATUS" -ne 0 && output_has "run it with sudo"'
fi

rm -rf "$DEVROOT"
dev dev-install-daemon.sh --build "$TMP/devbuild"
mkdir -p "$DEVROOT/var/lib/plaitway/profiles"
dev dev-uninstall-daemon.sh --dry-run
expect "dev-uninstall --dry-run changes nothing" \
    'test "$STATUS" -eq 0 -a -e "$DEVROOT/etc/systemd/system/plaitwayd.service" && output_has "nothing was changed"'
dev dev-uninstall-daemon.sh
expect "dev-uninstall removes the unit, the copies and the run directory" \
    'test "$STATUS" -eq 0 -a ! -e "$DEVROOT/etc/systemd/system/plaitwayd.service" -a ! -e "$DEVROOT/usr/local/libexec/plaitway" -a ! -e "$DEVROOT/usr/local/bin/plaitway" -a ! -e "$DEVROOT/run/plaitway"'
expect "dev-uninstall stops and disables the unit before it reloads systemd" 'line_before "systemctl disable --now plaitwayd.service" "daemon-reload"'
expect "dev-uninstall keeps the profiles" 'test -d "$DEVROOT/var/lib/plaitway/profiles"'
dev dev-uninstall-daemon.sh
expect "dev-uninstall with nothing installed succeeds" 'test "$STATUS" -eq 0'
dev dev-uninstall-daemon.sh --purge
expect "dev-uninstall --purge deletes the profiles" 'test "$STATUS" -eq 0 -a ! -e "$DEVROOT/var/lib/plaitway"'

# The commands that add the apt repository are in the README and on every release page.
source_line() { grep -h '^ *echo "deb \[signed-by=' "$1" | sed 's/^ *//'; }
expect "the apt source of the release page is the one of the README" \
    '[ -n "$(source_line "$ROOT/README.md")" ] && [ "$(source_line "$ROOT/README.md")" = "$(source_line "$ROOT/packaging/release-notes.md")" ]'

if [ "$FAILURES" -ne 0 ]; then
    printf '%s checks failed\n' "$FAILURES"
    exit 1
fi
echo "all checks passed"
