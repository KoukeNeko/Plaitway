#!/bin/sh
# Acceptance test of build/linux/plaitway_<version>_<arch>.deb (or the package
# given as the first argument), without installing it: the metadata, the files
# against the install map, the maintainer scripts, the unit, the files desktop
# environments read, the Python package, and the packaged daemon (-fake) and
# client started from an unpacked copy. CI runs it after build-deb.sh.
#
#   packaging/linux/verify-deb.sh [path/to/plaitway.deb]
#
# It reports every failure, then exits 1 if there was any. A check that needs a
# tool or a session the machine lacks says so and is skipped, never passed. It
# unpacks into a scratch directory, starts the packaged daemon with -fake on a
# temporary socket and stops it again, and, where the user has a systemd
# instance, runs it as a transient user service; it changes nothing else, and
# never runs the maintainer scripts.
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib.sh
. "$HERE/lib.sh"

read_version
ARCH="$(dpkg --print-architecture)"
DEB="${1:-$BUILD_DIR/plaitway_${VERSION}_${ARCH}.deb}"
[ -f "$DEB" ] || die "$DEB does not exist; run packaging/linux/build-deb.sh"

CHECKS=0
FAILURES=0
SKIPPED=0
SCRATCH="$(mktemp -d)"
DAEMON_PID=""
UNIT_RUN=plaitway-verify-$$
cleanup() {
    if [ -n "$DAEMON_PID" ]; then
        kill -KILL "$DAEMON_PID" 2>/dev/null
    fi
    systemctl --user stop "$UNIT_RUN.service" >/dev/null 2>&1
    systemctl --user reset-failed "$UNIT_RUN.service" >/dev/null 2>&1
    rm -rf "$SCRATCH"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

pass() { CHECKS=$((CHECKS + 1)); printf 'ok    %s\n' "$1"; }
fail() { CHECKS=$((CHECKS + 1)); FAILURES=$((FAILURES + 1)); printf 'FAIL  %s\n' "$1"; }
skip() { SKIPPED=$((SKIPPED + 1)); printf 'skip  %s (%s)\n' "$1" "$2"; }
# check DESCRIPTION CONDITION: passes when the shell code CONDITION succeeds; its
# output is shown when it does not.
check() {
    if OUT="$(eval "$2" 2>&1)"; then
        pass "$1"
    else
        fail "$1"
        [ -n "$OUT" ] && printf '%s\n' "$OUT" | sed 's/^/        /'
    fi
}
have() { command -v "$1" >/dev/null 2>&1; }
field() { dpkg-deb --field "$DEB" "$1"; }

TREE="$SCRATCH/tree"
mkdir -p "$TREE"
dpkg-deb --extract "$DEB" "$TREE" || die "cannot unpack $DEB"
dpkg-deb --control "$DEB" "$TREE/DEBIAN" || die "cannot unpack the control files of $DEB"
LIBEXEC="$TREE/usr/libexec/$LIBEXEC_NAME/plaitwayd"
CLI="$TREE/usr/bin/plaitway"
UNIT="$TREE/usr/lib/systemd/system/$UNIT_NAME"
# shellcheck disable=SC2034 # read by the commands that check and expect evaluate
DIST="$TREE/usr/lib/python3/dist-packages"
SHARE="$TREE/usr/share"

echo "== metadata"
check "the package is plaitway $VERSION for $ARCH" '[ "$(field Package)" = plaitway ] && [ "$(field Version)" = "$VERSION" ] && [ "$(field Architecture)" = "$ARCH" ]'
check "the control file names a maintainer, a section, a homepage and a description" '[ -n "$(field Maintainer)" ] && [ -n "$(field Section)" ] && [ -n "$(field Homepage)" ] && [ -n "$(field Description)" ]'
for dependency in libc6 openvpn systemd python3 python3-gi gir1.2-gtk-4.0 gir1.2-adw-1 gir1.2-secret-1 python3-grpcio python3-protobuf; do
    check "Depends has $dependency" 'field Depends | tr "," "\n" | sed "s/^ *//; s/[ (].*//" | grep -qx "$dependency"'
done
check "Depends has no package that the package itself would have to provide" '! field Depends | grep -q "plaitway"'
check "Recommends has systemd-resolved, polkitd and a Secret Service" 'field Recommends | grep -q systemd-resolved && field Recommends | grep -q polkitd && field Recommends | grep -q "gnome-keyring | kwallet6"'
check "Suggests has an AppIndicator extension" 'field Suggests | grep -q appindicator'
if have apt-get; then
    # Nothing is installed or changed: apt only solves the dependencies against
    # the package lists of this machine, which also checks that every package named
    # in the control file exists there.
    check "apt can install the package: every dependency exists and can be met (apt-get --simulate)" \
        'apt-get --simulate -o Debug::NoLocking=1 install "$DEB" | grep -q "^Inst plaitway "'
else
    skip "apt can install the package (apt-get --simulate)" "apt-get is not installed"
fi
check "the installed size is at least the size of the files" '[ "$(field Installed-Size)" -ge "$(( $(du -sk "$TREE/usr" | cut -f1) - 8 ))" ]'

echo "== contents"
LISTING="$SCRATCH/listing"
dpkg-deb --contents "$DEB" >"$LISTING"
# dpkg-deb --contents prints: permissions owner/group size date time path.
only_root_owns() { ! awk '$2 != "root/root"' "$LISTING" | grep -q .; }
no_setid_or_sticky() { ! awk '{ p = $1; if (substr(p, 4, 1) ~ /[sS]/ || substr(p, 7, 1) ~ /[sS]/ || substr(p, 10, 1) ~ /[tT]/) print }' "$LISTING" | grep -q .; }
nothing_writable_by_others() { ! awk '$1 !~ /^l/ && (substr($1, 6, 1) == "w" || substr($1, 9, 1) == "w")' "$LISTING" | grep -q .; }
everything_below_usr() { ! awk '$6 != "./" && $6 !~ /^\.\/usr(\/|$)/' "$LISTING" | grep -q .; }
check "every entry belongs to root:root" only_root_owns
check "no entry is setuid, setgid or sticky" no_setid_or_sticky
check "nothing but a symbolic link is writable by group or others" nothing_writable_by_others
check "everything is below /usr" everything_below_usr
check "the daemon is in libexec, is executable, and is not on PATH" '[ -x "$LIBEXEC" ] && [ ! -e "$TREE/usr/bin/plaitwayd" ] && [ ! -e "$TREE/usr/sbin/plaitwayd" ]'

# The files must be what install.sh lays out, given the package's own programs,
# notices and changelog: the install map is in one place, and the package is
# not allowed to drift from it.
EXPECTED_BUILD="$SCRATCH/build"
EXPECTED="$SCRATCH/expected"
mkdir -p "$EXPECTED_BUILD/bin"
cp "$LIBEXEC" "$EXPECTED_BUILD/bin/plaitwayd"
cp "$CLI" "$EXPECTED_BUILD/bin/plaitway"
cp "$SHARE/doc/plaitway/THIRD_PARTY_NOTICES.md" "$EXPECTED_BUILD/"
gzip -dc "$SHARE/doc/plaitway/changelog.gz" >"$EXPECTED_BUILD/changelog"
check "install.sh runs on the package's programs" 'sh "$HERE/install.sh" --prefix /usr --destdir "$EXPECTED" --build "$EXPECTED_BUILD" --version "$VERSION" >/dev/null'
tree_listing() { (cd "$1" && find usr \( -type f -o -type l -o -type d \) -printf '%y %m %p\n' | LC_ALL=C sort); }
same_files_and_modes() {
    tree_listing "$EXPECTED" >"$SCRATCH/expected.list"
    tree_listing "$TREE" >"$SCRATCH/actual.list"
    diff "$SCRATCH/expected.list" "$SCRATCH/actual.list"
}
check "the files, modes and directories are those of the install map" same_files_and_modes
check "the contents of the files are those of the install map (the changelog compared unpacked)" \
    'diff -r -x changelog.gz "$EXPECTED/usr" "$TREE/usr" && gzip -dc "$EXPECTED/usr/share/doc/plaitway/changelog.gz" | cmp - "$EXPECTED_BUILD/changelog"'
check "md5sums lists every file and every sum is right" '(cd "$TREE" && md5sum --check --quiet DEBIAN/md5sums) && [ "$(wc -l <"$TREE/DEBIAN/md5sums")" -eq "$(find "$TREE/usr" -type f | wc -l)" ]'

echo "== documents"
# The text of the license in the copyright file is the one in LICENSE, below its
# copyright line, apart from the layout (a leading space, "." for a blank line).
license_text_matches() {
    in_copyright="$(awk -v RS= '/^License: MIT\n /' "$SHARE/doc/plaitway/copyright" | sed '1d; s/^ //; s/^\.$//' | tr -s ' \n' ' ')"
    in_license="$(sed -n '/^Permission is hereby/,$p' "$ROOT/LICENSE" | tr -s ' \n' ' ')"
    [ -n "$in_license" ] && [ "$in_copyright" = "$in_license" ]
}
check "the copyright file is in the machine-readable format" 'head -n 1 "$SHARE/doc/plaitway/copyright" | grep -q "^Format: https://www.debian.org/doc/packaging-manuals/copyright-format/1.0/"'
check "the copyright file carries the MIT license of LICENSE" license_text_matches
check "the copyright file says OpenVPN is not part of the package" 'grep -q "OpenVPN is not part of this package" "$SHARE/doc/plaitway/copyright"'
check "the notices list the Go modules, say OpenVPN is not bundled, and make no source offer" \
    'grep -q "^## Go modules compiled into plaitwayd and plaitway" "$SHARE/doc/plaitway/THIRD_PARTY_NOTICES.md" && grep -q "OpenVPN is not part of this package" "$SHARE/doc/plaitway/THIRD_PARTY_NOTICES.md" && ! grep -q "^## Source offer" "$SHARE/doc/plaitway/THIRD_PARTY_NOTICES.md"'
if have dpkg-parsechangelog; then
    check "the changelog is in the format of Debian and is for $VERSION" '[ "$(gzip -dc "$SHARE/doc/plaitway/changelog.gz" | dpkg-parsechangelog -l- --show-field Version)" = "$VERSION" ]'
else
    skip "the changelog is parsed" "dpkg-parsechangelog is not installed"
fi

echo "== programs"
# The home directory is looked for only where it names something of the machine that built the
# package: "/root" is a word of the Go standard library, and a build as root in a container has it
# for a home.
case "${HOME:-}" in
    "" | / | /root) HOME_PATH="$ROOT" ;;
    *) HOME_PATH="$HOME" ;;
esac
# shellcheck disable=SC2034 # read by the commands that check evaluates
HOME_PATH="$HOME_PATH"
for program in "$LIBEXEC" "$CLI"; do
    name="$(basename "$program")"
    if have readelf; then
        check "$name links nothing but the C library" '[ "$(readelf -d "$program" | sed -n "s/.*(NEEDED).*\[\(.*\)\]/\1/p" | sort -u | tr "\n" " ")" = "libc.so.6 " ]'
    else
        skip "$name links nothing but the C library" "readelf is not installed"
    fi
    check "$name contains no path of the machine that built it" '! grep -aF -e "$ROOT" -e "$HOME_PATH" "$program"'
done

echo "== maintainer scripts"
for script in postinst prerm postrm; do
    check "$script is an executable POSIX shell script" '[ -x "$TREE/DEBIAN/$script" ] && head -n 1 "$TREE/DEBIAN/$script" | grep -qx "#!/bin/sh" && dash -n "$TREE/DEBIAN/$script"'
done
if have shellcheck; then
    check "the maintainer scripts pass shellcheck" 'shellcheck -s sh -S warning "$TREE/DEBIAN/postinst" "$TREE/DEBIAN/prerm" "$TREE/DEBIAN/postrm"'
else
    skip "the maintainer scripts pass shellcheck" "shellcheck is not installed"
fi

echo "== unit"
# The unit names the installed daemon by its package path; a copy that names the
# unpacked one lets systemd-analyze find it.
sed "s|^ExecStart=/usr/libexec/|ExecStart=$TREE/usr/libexec/|" "$UNIT" >"$SCRATCH/$UNIT_NAME"
if have systemd-analyze; then
    check "systemd-analyze verify accepts the unit" 'systemd-analyze verify "$SCRATCH/$UNIT_NAME"'
    if systemd-analyze security --offline=yes "$SCRATCH/$UNIT_NAME" >/dev/null 2>&1; then
        check "the exposure of the unit (systemd-analyze security --offline) is below 4" \
            'systemd-analyze security --offline=yes --threshold=4 --no-pager "$SCRATCH/$UNIT_NAME" | tail -n 1'
    else
        skip "the exposure of the unit" "this systemd-analyze cannot judge a unit offline"
    fi
else
    skip "systemd-analyze verifies the unit" "systemd-analyze is not installed"
fi
check "the unit is a notifying service, restarted on failure, wanted by multi-user.target" \
    'grep -qx "Type=notify" "$UNIT" && grep -qx "Restart=on-failure" "$UNIT" && grep -qx "WantedBy=multi-user.target" "$UNIT"'
check "the unit gives the daemon the production paths" \
    'grep -qx "ExecStart=/usr/libexec/plaitway/plaitwayd -socket /run/plaitway/plaitwayd.sock -socket-mode 0666 -state-dir /var/lib/plaitway -run-dir /run/plaitway -openvpn /usr/sbin/openvpn" "$UNIT" && grep -qx "RuntimeDirectory=plaitway" "$UNIT" && grep -qx "StateDirectory=plaitway" "$UNIT" && grep -qx "StateDirectoryMode=0700" "$UNIT"'
check "TimeoutStopSec is above the daemon's shutdown budget" \
    '[ "$(unit_stop_budget "$UNIT")" -gt "$(daemon_stop_budget "$ROOT/cmd/plaitwayd/daemon.go")" ]'

echo "== files that desktop environments read"
# shellcheck disable=SC2034 # read by the commands that check and expect evaluate
DESKTOP="$SHARE/applications/io.github.koukeneko.Plaitway.desktop"
# shellcheck disable=SC2034 # read by the commands that check and expect evaluate
METAINFO="$SHARE/metainfo/io.github.koukeneko.Plaitway.metainfo.xml"
# shellcheck disable=SC2034 # read by the commands that check and expect evaluate
MIME="$SHARE/mime/packages/io.github.koukeneko.Plaitway.xml"
if have desktop-file-validate; then
    check "the desktop entry validates" 'desktop-file-validate "$DESKTOP"'
else
    skip "the desktop entry validates" "desktop-file-validate is not installed"
fi
if have appstreamcli; then
    check "the metainfo validates" 'appstreamcli validate --no-net "$METAINFO"'
else
    skip "the metainfo validates" "appstreamcli is not installed"
fi
if have xmllint; then
    check "the MIME type file is well-formed XML" 'xmllint --noout "$MIME"'
else
    skip "the MIME type file is well-formed XML" "xmllint is not installed"
fi
check "the desktop entry starts plaitway-app, which is installed, and its icon exists" \
    'grep -qx "Exec=plaitway-app %F" "$DESKTOP" && [ -x "$TREE/usr/bin/plaitway-app" ] && [ -f "$SHARE/icons/hicolor/256x256/apps/io.github.koukeneko.Plaitway.png" ]'
check "the seven state icons of the tray are installed" '[ "$(ls "$SHARE"/icons/hicolor/symbolic/apps/plaitway-state-*-symbolic.svg | wc -l)" -eq 7 ]'

echo "== Python package"
PYTHON="${PLAITWAY_PYTHON:-python3}"
check "the launcher uses the system's Python" 'head -n 1 "$TREE/usr/bin/plaitway-app" | grep -qx "#!/usr/bin/python3"'
if have "$PYTHON" && "$PYTHON" -I -c 'import grpc, google.protobuf' 2>/dev/null; then
    check "every module compiles" '"$PYTHON" -I -m compileall -q "$DIST/plaitway"'
    check "the package reports version $VERSION" \
        '"$PYTHON" -I -c "import sys; sys.path.insert(0, sys.argv[1]); from plaitway import version; print(version.__version__)" "$DIST" | grep -qx "$VERSION"'
    check "the client and the model import, and the API descriptor loads" \
        '"$PYTHON" -I -c "import sys; sys.path.insert(0, sys.argv[1]); import plaitway.client.daemon_client, plaitway.core.app_model, plaitway.l10n.strings; from plaitway.client import proto" "$DIST"'
    if "$PYTHON" -I -c 'import gi; gi.require_version("Gtk", "4.0"); gi.require_version("Adw", "1"); gi.require_version("Secret", "1")' 2>/dev/null; then
        check "the GTK app's modules import (GTK 4, libadwaita, libsecret)" \
            '"$PYTHON" -I -c "
import importlib, pkgutil, sys
sys.path.insert(0, sys.argv[1])
import plaitway.app
for module in pkgutil.walk_packages(plaitway.app.__path__, \"plaitway.app.\"):
    importlib.import_module(module.name)
import plaitway.app.main
" "$DIST"'
    else
        skip "the GTK app's modules import" "$PYTHON has no GTK 4, libadwaita 1 and libsecret bindings"
    fi
else
    skip "the Python package is imported" "$PYTHON lacks grpc or protobuf (apt install python3-grpcio python3-protobuf, or set PLAITWAY_PYTHON)"
fi

echo "== packaged daemon and client"
# The notification socket stands in for systemd's: the daemon sends READY=1 once
# it listens and STOPPING=1 when it begins to stop.
cat >"$SCRATCH/notify.py" <<'PY'
import os, socket, sys
path, ready_file = sys.argv[1], sys.argv[2]
s = socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM)
s.bind(path)
s.settimeout(15)
with open(ready_file, "w") as f:
    f.write("bound\n")
for _ in range(2):
    print(s.recv(256).decode(), flush=True)
PY
if (cd "$ROOT" && go build -o "$SCRATCH/smoketest" ./packaging/smoketest) 2>"$SCRATCH/build.log"; then
    SOCK="$SCRATCH/s.sock"
    NOTIFY="$SCRATCH/n.sock"
    python3 -I "$SCRATCH/notify.py" "$NOTIFY" "$SCRATCH/notify-bound" >"$SCRATCH/notified" 2>&1 &
    NOTIFIER=$!
    for _ in 1 2 3 4 5 6 7 8 9 10; do [ -e "$SCRATCH/notify-bound" ] && break; sleep 0.2; done
    NOTIFY_SOCKET="$NOTIFY" "$LIBEXEC" -fake -socket "$SOCK" -socket-mode 0600 \
        -state-dir "$SCRATCH/state" -run-dir "$SCRATCH/run" >"$SCRATCH/daemon.log" 2>&1 &
    DAEMON_PID=$!
    if INFO="$("$SCRATCH/smoketest" -socket "$SOCK" -timeout 10s 2>&1)"; then
        pass "GetDaemonInfo answered"
        check "the daemon reports version $VERSION" 'printf "%s\n" "$INFO" | grep -qx "version=$VERSION"'
        check "the packaged plaitway reports version $VERSION" '[ "$("$CLI" version)" = "plaitway $VERSION" ]'
        check "the packaged plaitway lists the profiles of the packaged daemon" '"$CLI" -socket "$SOCK" list'
        check "the packaged plaitway imports a WireGuard profile into it" \
            'printf "[Interface]\nPrivateKey = %s\nAddress = 10.9.0.2/32\n\n[Peer]\nPublicKey = %s\nAllowedIPs = 10.9.0.0/24\nEndpoint = 192.0.2.1:51820\n" "cEZoRYPHSm4bGj6TuvmHkL3vMdqdXczGlxuZW0Ji2U8=" "9U3pMLwkDvEWRHsFVnnYxWmdCaZ1wQK3JQfY7mC1x2E=" >"$SCRATCH/test.conf" && "$CLI" -socket "$SOCK" import "$SCRATCH/test.conf" && "$CLI" -socket "$SOCK" list | grep -q WireGuard'
    else
        fail "GetDaemonInfo failed: $INFO"
        sed 's/^/        /' "$SCRATCH/daemon.log"
    fi
    kill -TERM "$DAEMON_PID" 2>/dev/null
    for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do kill -0 "$DAEMON_PID" 2>/dev/null || break; sleep 0.5; done
    if kill -0 "$DAEMON_PID" 2>/dev/null; then
        fail "the daemon did not exit within 10 s of SIGTERM"
        sed 's/^/        /' "$SCRATCH/daemon.log"
    elif wait "$DAEMON_PID"; then
        pass "the daemon exits with status 0 on SIGTERM"
    else
        fail "the daemon exited with status $? on SIGTERM"
        sed 's/^/        /' "$SCRATCH/daemon.log"
    fi
    DAEMON_PID=""
    wait "$NOTIFIER" 2>/dev/null
    check "the daemon told the notification socket READY=1 and then STOPPING=1" '[ "$(tr "\n" " " <"$SCRATCH/notified")" = "READY=1 STOPPING=1 " ]'
else
    fail "could not build packaging/smoketest"
    sed 's/^/        /' "$SCRATCH/build.log"
fi

echo "== packaged unit as a transient user service"
# The unit's [Service] settings, run for the user by the user's own systemd with
# the -fake daemon: readiness, the run and state directories, the modes, the
# stop. The settings that need the system manager (the capability bounding set,
# and the Protect* options that drop capabilities) are left out, because a user
# manager cannot set them; the mount namespace options are accepted and not
# applied. Skipped where there is no user manager.
if have systemd-run && timeout 10 systemd-run --user --quiet --wait --collect true >/dev/null 2>&1; then
    USER_RUN="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/plaitway-verify-$$"
    # The properties go through the positional parameters: a value has spaces.
    set -f
    set --
    while IFS= read -r setting; do
        set -- "$@" -p "$setting"
    done <<PROPS
$(awk '/^\[Service\]/ { in_service = 1; next } /^\[/ { in_service = 0 } in_service && /^[A-Za-z]+=/ && !/^(ExecStart|CapabilityBoundingSet|ProtectKernelModules|ProtectKernelLogs|ProtectClock|RuntimeDirectory|StateDirectory)=/' "$UNIT")
PROPS
    set +f
    if systemd-run --user --quiet --unit="$UNIT_RUN" --service-type=notify "$@" \
        -p "RuntimeDirectory=plaitway-verify-$$" -p "StateDirectory=plaitway-verify-$$" \
        "$LIBEXEC" -fake -socket "$USER_RUN/plaitwayd.sock" -socket-mode 0666 \
        -state-dir "${XDG_STATE_HOME:-$HOME/.local/state}/plaitway-verify-$$" -run-dir "$USER_RUN" >"$SCRATCH/user-run.log" 2>&1; then
        check "systemd reports the service started, and active (it waited for READY=1)" '[ "$(systemctl --user show "$UNIT_RUN.service" -p ActiveState --value)" = active ]'
        check "the socket exists and the daemon answers" '"$SCRATCH/smoketest" -socket "$USER_RUN/plaitwayd.sock" -timeout 10s'
        check "the run directory is 0755 and the state directory 0700" \
            '[ "$(stat -c %a "$USER_RUN")" = 755 ] && [ "$(stat -c %a "${XDG_STATE_HOME:-$HOME/.local/state}/plaitway-verify-$$")" = 700 ]'
        check "stopping the service ends it with success and removes the run directory" \
            'systemctl --user stop "$UNIT_RUN.service" && [ "$(systemctl --user show "$UNIT_RUN.service" -p Result --value)" = success ] && [ ! -e "$USER_RUN" ]'
        rm -rf "${XDG_STATE_HOME:-$HOME/.local/state}/plaitway-verify-$$"
    else
        fail "systemd-run could not start the unit's settings with the packaged daemon"
        sed 's/^/        /' "$SCRATCH/user-run.log"
        systemctl --user status "$UNIT_RUN.service" 2>&1 | sed 's/^/        /' | head -n 20
    fi
else
    skip "the packaged unit runs as a transient user service" "no systemd user instance answers (systemd-run --user)"
fi

echo
echo "$CHECKS checks, $FAILURES failed, $SKIPPED skipped"
[ "$FAILURES" -eq 0 ]
