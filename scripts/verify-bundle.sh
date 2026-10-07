#!/bin/bash
# Packaging acceptance test for build/Plaitway.app (or the app given as the
# first argument): structure, plists, every Mach-O, signatures, the notices and
# the source offer, the bundled OpenVPN and the hash of it that the daemon has
# built in, a start of the packaged daemon with the packaged command line
# client, and (for build/Plaitway.app) the zip and dmg made from it.
#
#   scripts/verify-bundle.sh [path/to/Plaitway.app]
#
# It reports every failure, then exits 1 if there was any. It starts the
# packaged daemon with -fake on a temporary socket and stops it again, and it
# mounts build/Plaitway-<version>.dmg read-only for a moment; it changes
# nothing else.
set -o pipefail
# shellcheck source=../packaging/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/../packaging/lib.sh"
# shellcheck source=../packaging/openvpn-deps.env
source "$PACKAGING_DIR/openvpn-deps.env"

APP="${1:-$BUILD_DIR/Plaitway.app}"
# The zip and dmg are checked against the app they were made from only when
# that is the app of the build directory.
CHECK_DIST=0
[ $# -eq 0 ] && CHECK_DIST=1
read_version
[ -d "$APP" ] || die "$APP does not exist; run make app"

INFO_PLIST="$APP/Contents/Info.plist"
DAEMON="$APP/Contents/MacOS/plaitwayd"
DAEMON_PLIST="$APP/Contents/Library/LaunchDaemons/$DAEMON_LABEL.plist"
OPENVPN="$APP/Contents/Resources/bin/openvpn"
CLI="$APP/Contents/Resources/bin/plaitway"
NOTICES="$APP/Contents/Resources/THIRD_PARTY_NOTICES.md"
SOURCE_DIR="$APP/Contents/Resources/Source"
SOCKET_PATH_MAX=104 # sun_path on macOS, including the terminating NUL

CHECKS=0
FAILURES=0
SCRATCH=""
DAEMON_PID=""
DMG_MOUNT=""

cleanup() {
    if [ -n "$DAEMON_PID" ]; then
        kill -KILL "$DAEMON_PID" 2>/dev/null
    fi
    if [ -n "$DMG_MOUNT" ]; then
        hdiutil detach "$DMG_MOUNT" -force -quiet 2>/dev/null
    fi
    if [ -n "$SCRATCH" ]; then
        rm -rf "$SCRATCH"
    fi
}
trap cleanup EXIT

pass() { CHECKS=$((CHECKS + 1)); printf 'ok    %s\n' "$1"; }
fail() { CHECKS=$((CHECKS + 1)); FAILURES=$((FAILURES + 1)); printf 'FAIL  %s\n' "$1"; }

# check DESCRIPTION COMMAND...: passes when COMMAND succeeds; its output is
# shown when it does not.
check() {
    local description=$1 output
    shift
    if output="$("$@" 2>&1)"; then
        pass "$description"
    else
        fail "$description"
        [ -n "$output" ] && printf '%s\n' "$output" | sed 's/^/        /'
    fi
}

# expect_plist DESCRIPTION FILE KEYPATH EXPECTED
expect_plist() {
    local actual
    if ! actual="$(plutil -extract "$3" raw -o - "$2" 2>&1)"; then
        fail "$1: $actual"
    elif [ "$actual" = "$4" ]; then
        pass "$1 is $4"
    else
        fail "$1: got '$actual', want '$4'"
    fi
}

version_le() { [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | head -n 1)" = "$1" ]; }

echo "== structure"
for entry in Contents/Info.plist Contents/Resources/Plaitway.icns Contents/Resources/THIRD_PARTY_NOTICES.md \
    Contents/Resources/Credits.rtf "Contents/Library/LaunchDaemons/$DAEMON_LABEL.plist"; do
    check "$entry exists" test -f "$APP/$entry"
done
for entry in Contents/MacOS/Plaitway Contents/MacOS/plaitwayd Contents/Resources/bin/openvpn Contents/Resources/bin/plaitway; do
    check "$entry is executable" test -x "$APP/$entry"
done

echo "== Info.plist"
check "Info.plist passes plutil -lint" plutil -lint "$INFO_PLIST"
expect_plist "CFBundleIdentifier" "$INFO_PLIST" CFBundleIdentifier "$BUNDLE_ID"
expect_plist "CFBundleExecutable" "$INFO_PLIST" CFBundleExecutable Plaitway
expect_plist "CFBundlePackageType" "$INFO_PLIST" CFBundlePackageType APPL
expect_plist "CFBundleShortVersionString" "$INFO_PLIST" CFBundleShortVersionString "$VERSION"
expect_plist "CFBundleVersion" "$INFO_PLIST" CFBundleVersion "$VERSION"
expect_plist "CFBundleIconFile" "$INFO_PLIST" CFBundleIconFile Plaitway
expect_plist "LSUIElement" "$INFO_PLIST" LSUIElement true
expect_plist "LSMinimumSystemVersion" "$INFO_PLIST" LSMinimumSystemVersion "$MIN_MACOS"
check "NSHumanReadableCopyright is set" plutil -extract NSHumanReadableCopyright raw -o - "$INFO_PLIST"

echo "== daemon plist"
check "daemon plist passes plutil -lint" plutil -lint "$DAEMON_PLIST"
expect_plist "Label" "$DAEMON_PLIST" Label "$DAEMON_LABEL"
expect_plist "BundleProgram" "$DAEMON_PLIST" BundleProgram Contents/MacOS/plaitwayd
expect_plist "RunAtLoad" "$DAEMON_PLIST" RunAtLoad true
expect_plist "KeepAlive" "$DAEMON_PLIST" KeepAlive true
expect_plist "ExitTimeOut" "$DAEMON_PLIST" ExitTimeOut "$DAEMON_EXIT_TIMEOUT"
expect_plist "AssociatedBundleIdentifiers.0" "$DAEMON_PLIST" AssociatedBundleIdentifiers.0 "$BUNDLE_ID"
if plutil -extract Program raw -o - "$DAEMON_PLIST" >/dev/null 2>&1; then
    fail "the daemon plist has an absolute Program key next to BundleProgram"
else
    pass "BundleProgram is the only program key"
fi
check "file name is Label.plist" test "$(basename "$DAEMON_PLIST")" = "$DAEMON_LABEL.plist"
if plutil -extract StandardErrorPath raw -o - "$DAEMON_PLIST" >/dev/null 2>&1; then
    fail "the daemon plist sets StandardErrorPath: launchd fails the job when that directory is missing, the daemon logs itself"
else
    pass "no StandardErrorPath (the daemon creates $LOG_DIR and logs there)"
fi
daemon_args=()
index=0
while arg="$(plutil -extract "ProgramArguments.$index" raw -o - "$DAEMON_PLIST" 2>/dev/null)"; do
    daemon_args[${#daemon_args[@]}]="$arg"
    index=$((index + 1))
done
expect_flag() { # FLAG VALUE
    local i
    for ((i = 0; i + 1 < ${#daemon_args[@]}; i++)); do
        if [ "${daemon_args[$i]}" = "$1" ] && [ "${daemon_args[$((i + 1))]}" = "$2" ]; then
            pass "ProgramArguments has $1 $2"
            return
        fi
    done
    fail "ProgramArguments lacks $1 $2 (has: ${daemon_args[*]})"
}
expect_flag -socket "$SOCKET_PATH"
expect_flag -socket-mode 0666
expect_flag -state-dir "$STATE_DIR"
expect_flag -run-dir "$RUN_DIR"
if [ "${#SOCKET_PATH}" -lt "$SOCKET_PATH_MAX" ]; then
    pass "socket path fits in sun_path"
else
    fail "socket path is ${#SOCKET_PATH} bytes, the limit is $((SOCKET_PATH_MAX - 1))"
fi

echo "== Mach-O files"
macho_files=()
while IFS= read -r -d '' file; do
    kind="$(file -b "$file")"
    if [[ "$kind" == *Mach-O* ]]; then
        macho_files[${#macho_files[@]}]="$file"
    fi
done < <(find "$APP" -type f -print0)
check "the bundle contains Mach-O files" test "${#macho_files[@]}" -gt 0

team_ids=""
for file in "${macho_files[@]}"; do
    name="${file#"$APP"/}"
    check "$name is arm64 only" test "$(lipo -archs "$file")" = arm64

    own_id="$(otool -D "$file" | sed -n '2p')"
    foreign=""
    while IFS= read -r dep; do
        [ "$dep" = "$own_id" ] && continue
        case "$dep" in
            /usr/lib/* | /System/Library/*) ;;
            @rpath/*.dylib) [ -f "$APP/Contents/Frameworks/${dep#@rpath/}" ] || foreign="$foreign $dep" ;;
            *) foreign="$foreign $dep" ;;
        esac
    done < <(otool -L "$file" | tail -n +2 | awk '{print $1}')
    if [ -z "$foreign" ]; then
        pass "$name links only system libraries or Contents/Frameworks"
    else
        fail "$name links against$foreign"
    fi

    minos="$(otool -l "$file" | awk '/LC_BUILD_VERSION/ { found = 1 } found && /minos/ { print $2; exit }')"
    if [ -n "$minos" ] && version_le "$minos" "$MIN_MACOS"; then
        pass "$name targets macOS $minos or later"
    else
        fail "$name has minimum macOS '$minos', want at most $MIN_MACOS"
    fi

    leaks="$(build_path_leaks "$file")" || leaks="cannot scan the strings of the file"
    if [ -z "$leaks" ]; then
        pass "$name names no directory of the build machine"
    else
        fail "$name names directories of the build machine: $(printf '%s\n' "$leaks" | head -n 3 | cut -c1-120 | tr '\n' ' ')"
    fi

    check "$name passes codesign --verify --strict" codesign --verify --strict "$file"
    details="$(codesign -dvv "$file" 2>&1)"
    if grep -q 'flags=.*runtime' <<<"$details"; then
        pass "$name has the hardened runtime"
    else
        fail "$name lacks the hardened runtime flag"
    fi
    if grep -q '^Signature=adhoc' <<<"$details"; then
        printf 'note  %s is signed ad hoc: no Developer ID, no timestamp\n' "$name"
    else
        if grep -q '^Authority=Developer ID Application' <<<"$details"; then
            pass "$name is signed with a Developer ID Application certificate"
        else
            fail "$name is not signed with a Developer ID Application certificate"
        fi
        if grep -q '^Timestamp=' <<<"$details"; then
            pass "$name has a secure timestamp"
        else
            fail "$name has no secure timestamp"
        fi
    fi
    team_ids="$team_ids
$(sed -n 's/^TeamIdentifier=//p' <<<"$details")"
    # Entitlements that switch off library validation or allow code injection
    # would defeat the hardened runtime in a root process.
    if codesign -d --entitlements - --xml "$file" 2>/dev/null |
        grep -E 'disable-library-validation|allow-dyld-environment-variables|get-task-allow|allow-unsigned-executable-memory|disable-executable-page-protection' >/dev/null; then
        fail "$name carries an entitlement that weakens the hardened runtime"
    else
        pass "$name has no runtime-weakening entitlement"
    fi
done
check "all Mach-O files share one Team ID" test "$(printf '%s\n' "$team_ids" | sed '/^$/d' | sort -u | wc -l | tr -d ' ')" -le 1

echo "== signatures"
check "codesign --verify --deep --strict on the app" codesign --verify --deep --strict --verbose=2 "$APP"
for pair in "Contents/MacOS/plaitwayd:$DAEMON_LABEL" "Contents/Resources/bin/openvpn:$OPENVPN_IDENTIFIER" \
    "Contents/Resources/bin/plaitway:$CLI_IDENTIFIER"; do
    path="${pair%%:*}"
    want="${pair#*:}"
    got="$(codesign -dvv "$APP/$path" 2>&1 | sed -n 's/^Identifier=//p')"
    if [ "$got" = "$want" ]; then
        pass "$path has identifier $want"
    else
        fail "$path has identifier '$got', want $want"
    fi
done
got="$(codesign -dvv "$APP" 2>&1 | sed -n 's/^Identifier=//p')"
if [ "$got" = "$BUNDLE_ID" ]; then
    pass "the app has identifier $BUNDLE_ID"
else
    fail "the app has identifier '$got', want $BUNDLE_ID"
fi
echo "spctl --assess (not notarized until packaging/notarize.sh has run):"
spctl --assess --type execute --verbose=4 "$APP" 2>&1 | sed 's/^/        /'

echo "== notices and source offer"
check "notices name the OpenVPN source URL" grep -qF "$OPENVPN_URL" "$NOTICES"
check "notices name the OpenVPN source SHA-256" grep -qF "$OPENVPN_SHA256" "$NOTICES"
check "notices make the GPLv2 written offer" grep -qF "Written offer, GPLv2 section 3(b)" "$NOTICES"
check "notices carry the trademark notice" grep -qF "## Trademarks" "$NOTICES"
check "Credits.rtf, which the About panel shows, is valid RTF" textutil -convert txt -stdout "$APP/Contents/Resources/Credits.rtf"
for binary in "$DAEMON" "$CLI"; do
    missing=""
    while read -r module; do
        grep -qF "$module" "$NOTICES" || missing="$missing $module"
    done < <(go version -m "$binary" | awk '$1 == "dep" { print $2 }')
    if [ -z "$missing" ]; then
        pass "notices list every Go module linked into $(basename "$binary")"
    else
        fail "notices lack the Go modules of $(basename "$binary"):$missing"
    fi
done
check "notices match packaging/THIRD_PARTY_NOTICES.md" cmp -s "$NOTICES" "$PACKAGING_DIR/THIRD_PARTY_NOTICES.md"
# The source that accompanies the binary: the archives of the pinned sources
# and the scripts that built openvpn, as they are in packaging/.
for pair in "$OPENVPN_URL:$OPENVPN_SHA256" "$LZO_URL:$LZO_SHA256" "$LZ4_URL:$LZ4_SHA256"; do
    url="${pair%:*}" # the URL has colons of its own: split at the last one
    archive="$SOURCE_DIR/$(basename "$url")"
    if [ ! -f "$archive" ]; then
        fail "Source/$(basename "$archive") is missing"
    elif [ "$(shasum -a 256 "$archive" | cut -d' ' -f1)" = "${pair##*:}" ]; then
        pass "Source/$(basename "$archive") has the pinned SHA-256"
    else
        fail "Source/$(basename "$archive") does not have the pinned SHA-256"
    fi
done
for script in build-openvpn.sh lib.sh openvpn-deps.env; do
    check "Source/$script is the packaging/$script that built openvpn" cmp -s "$SOURCE_DIR/$script" "$PACKAGING_DIR/$script"
done

SCRATCH="$(mktemp -d "${TMPDIR:-/tmp}/plaitway-verify.XXXXXX")"
SCRATCH="$(cd "$SCRATCH" && pwd -P)"

echo "== bundled OpenVPN"
version_output="$("$OPENVPN" --version 2>&1)"
version_line="${version_output%%$'\n'*}"
case "$version_line" in
    "OpenVPN $OPENVPN_VERSION "*"[LZO]"*"[LZ4]"*) pass "openvpn --version: $version_line" ;;
    *) fail "openvpn --version line is '$version_line', want $OPENVPN_VERSION with LZO and LZ4" ;;
esac
ciphers="$("$OPENVPN" --show-ciphers 2>&1)"
if grep -q '^AES-128-CBC ' <<<"$ciphers"; then
    pass "openvpn --show-ciphers lists AES-128-CBC"
else
    fail "openvpn --show-ciphers lacks AES-128-CBC"
fi
check "openvpn was built without the dns-updown helper by default" binary_has_string "$OPENVPN" enable_dns_updown_by_default=no

# The daemon runs, as root, only an openvpn with the SHA-256 it was built with
# (-X main.openvpnSHA256), and that has to be the hash of the signed file in
# this bundle. The linker puts the value into the binary as a 64-character
# string and nowhere else, and a daemon without the flag has none; the build
# info does not record linker flags, so the string itself is what can be found.
openvpn_sha="$(shasum -a 256 "$OPENVPN" | cut -d' ' -f1)"
occurrences="$(grep -ao "$openvpn_sha" "$DAEMON" | wc -l | tr -d ' ')"
if [ "$occurrences" -ge 1 ]; then
    pass "the daemon has the SHA-256 of the bundled, signed openvpn built in ($openvpn_sha)"
else
    fail "the daemon does not carry the SHA-256 of the bundled openvpn ($openvpn_sha): it was built for another openvpn, or without -X main.openvpnSHA256"
fi

# The ASUS router profile shape, with a throwaway self-signed certificate in
# place of the real inline blocks. openvpn must accept every option and get as
# far as trying to connect (it then fails: nothing listens on port 1).
/usr/bin/openssl req -x509 -newkey rsa:2048 -nodes -keyout "$SCRATCH/key.pem" -out "$SCRATCH/cert.pem" \
    -subj /CN=plaitway-verify -days 2 >/dev/null 2>&1
printf 'user\npass\n' >"$SCRATCH/auth.txt"
chmod 600 "$SCRATCH/auth.txt"
{
    cat <<'EOF'
client
dev tun
proto tcp-client
remote example.invalid 1194
float
nobind
sndbuf 0
rcvbuf 0
keepalive 10 30
comp-lzo yes
auth-user-pass
auth-nocache
auth SHA1
cipher AES-128-CBC
data-ciphers AES-128-CBC
ignore-unknown-option cipher data-ciphers
ignore-unknown-option block-outside-dns
remote-cert-tls server
pull-filter ignore "redirect-gateway"
pull-filter ignore "block-outside-dns"
pull-filter ignore "dhcp-option"
route 192.168.1.0 255.255.255.0
EOF
    for block in ca:cert cert:cert key:key; do
        printf '<%s>\n' "${block%%:*}"
        cat "$SCRATCH/${block#*:}.pem"
        printf '</%s>\n' "${block%%:*}"
    done
} >"$SCRATCH/asus.ovpn"
run_with_timeout 30 "$OPENVPN" --config "$SCRATCH/asus.ovpn" --auth-user-pass "$SCRATCH/auth.txt" --verb 3 \
    --remote 127.0.0.1 1 --dev null --connect-retry-max 1 --connect-timeout 2 >"$SCRATCH/openvpn.log" 2>&1
if grep -q 'Options error' "$SCRATCH/openvpn.log"; then
    fail "openvpn rejects the ASUS-style profile"
    sed 's/^/        /' "$SCRATCH/openvpn.log"
elif grep -q 'Attempting to establish TCP connection' "$SCRATCH/openvpn.log"; then
    pass "openvpn accepts the ASUS-style profile (comp-lzo, AES-128-CBC, auth SHA1) and tries to connect"
else
    fail "openvpn did not reach the connect step"
    sed 's/^/        /' "$SCRATCH/openvpn.log"
fi

echo "== packaged daemon"
if (cd "$ROOT" && go build -o "$SCRATCH/smoketest" ./packaging/smoketest) 2>"$SCRATCH/build.log"; then
    daemon_socket="$SCRATCH/plaitwayd.sock"
    "$DAEMON" -fake -socket "$daemon_socket" -socket-mode 0600 \
        -state-dir "$SCRATCH/state" -run-dir "$SCRATCH/run" >"$SCRATCH/daemon.log" 2>&1 &
    DAEMON_PID=$!
    if info="$("$SCRATCH/smoketest" -socket "$daemon_socket" -timeout 10s 2>&1)"; then
        pass "GetDaemonInfo answered"
        check "the packaged plaitway reports version $VERSION" test "$("$CLI" version 2>&1)" = "plaitway $VERSION"
        check "the packaged plaitway lists the profiles of the packaged daemon" "$CLI" -socket "$daemon_socket" list
        reported="$(printf '%s\n' "$info" | sed -n 's/^version=//p')"
        if [ "$reported" = "$VERSION" ]; then
            pass "the daemon reports version $VERSION"
        else
            fail "the daemon reports version '$reported', want $VERSION (is -X main.version applied?)"
        fi
    else
        fail "GetDaemonInfo failed: $info"
        sed 's/^/        /' "$SCRATCH/daemon.log"
    fi
    # Stop it the way launchd does and expect a clean exit.
    kill -TERM "$DAEMON_PID" 2>/dev/null
    stopped=0
    for _ in 1 2 3 4 5 6 7 8 9 10; do
        kill -0 "$DAEMON_PID" 2>/dev/null || { stopped=1; break; }
        sleep 0.5
    done
    if [ "$stopped" = 1 ] && wait "$DAEMON_PID"; then
        pass "the daemon exits cleanly on SIGTERM"
    else
        fail "the daemon did not exit cleanly on SIGTERM"
        sed 's/^/        /' "$SCRATCH/daemon.log"
    fi
    DAEMON_PID=""
else
    fail "could not build packaging/smoketest"
    sed 's/^/        /' "$SCRATCH/build.log"
fi

# check_dmg_matches_app DMG APP: DMG mounts read-only (no Finder window, a
# mount point in the scratch directory) with an app whose signature verifies
# and that is the build of APP. Prints the problem and returns 1 otherwise.
check_dmg_matches_app() {
    local dmg=$1 app=$2 inside status=0
    codesign --verify --strict "$dmg" 2>&1 || { echo "$dmg does not pass codesign --verify"; return 1; }
    mkdir -p "$SCRATCH/dmg"
    hdiutil attach -readonly -nobrowse -noautoopen -mountpoint "$SCRATCH/dmg" "$dmg" >/dev/null ||
        { echo "cannot mount $dmg"; return 1; }
    DMG_MOUNT="$SCRATCH/dmg"
    inside="$SCRATCH/dmg/$(basename "$app")"
    if ! codesign --verify --deep --strict "$inside" 2>&1; then
        echo "the app in $dmg fails codesign --verify"
        status=1
    elif [ "$(cdhash_of "$inside")" != "$(cdhash_of "$app")" ]; then
        echo "$dmg holds a different build than $app"
        status=1
    fi
    hdiutil detach "$DMG_MOUNT" -quiet || hdiutil detach "$DMG_MOUNT" -force -quiet || status=1
    DMG_MOUNT=""
    return "$status"
}

if [ "$CHECK_DIST" = 1 ]; then
    echo "== zip and dmg"
    for dist in "$BUILD_DIR/Plaitway-$VERSION.zip" "$BUILD_DIR/Plaitway-$VERSION.dmg"; do
        if [ ! -f "$dist" ]; then
            printf 'note  %s does not exist (make dist creates it)\n' "$(basename "$dist")"
        elif [ "${dist##*.}" = zip ]; then
            check "$(basename "$dist") has no AppleDouble entries and unpacks into this build of the app" check_zip_matches_app "$dist" "$APP"
        else
            check "$(basename "$dist") holds this build of the app" check_dmg_matches_app "$dist" "$APP"
        fi
    done
fi

echo
printf '%d checks, %d failed\n' "$CHECKS" "$FAILURES"
[ "$FAILURES" -eq 0 ]
