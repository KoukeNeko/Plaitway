#!/bin/bash
# Tests of the packaging scripts that need no build and no certificate: the
# signing helpers in lib.sh against a fake codesign (the watchdog that ends a
# codesign stuck on a keychain prompt, the no-timestamp refusal, no retry of
# other failures), make-dist.sh on a small ad hoc signed app, and the
# daemon's shutdown budget against the launchd ExitTimeOut.
set -uo pipefail
PACKAGING_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$PACKAGING_DIR/.." && pwd)"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
mkdir "$TMP/bin"
cat >"$TMP/bin/codesign" <<'FAKE'
#!/bin/bash
echo "$*" >>"$FAKE_CALLS"
case "$FAKE_MODE" in
    ok) exit 0 ;;
    timestamp-server-down)
        for arg in "$@"; do
            if [ "$arg" = --timestamp ]; then
                echo "The timestamp service is not available." >&2
                exit 1
            fi
        done ;;
    other-failure) echo "no identity found" >&2; exit 1 ;;
    hang) exec sleep 30 ;;
esac
FAKE
chmod +x "$TMP/bin/codesign"

FAILURES=0
STATUS=0
CALLS=0
OUTPUT=""

# sign MODE [IDENTITY]: runs sign_path with a 1 s watchdog. PLAITWAY_ALLOW_NO_TIMESTAMP
# passes through from the caller's environment.
sign() {
    : >"$TMP/calls"
    OUTPUT="$(FAKE_MODE="$1" FAKE_CALLS="$TMP/calls" PATH="$TMP/bin:$PATH" PLAITWAY_SIGN_IDENTITY="${2:-ABCDEF}" \
        bash -c 'source "$1"; SIGN_TIMEOUT_SECONDS=1; sign_path /dev/null io.example.test' _ "$PACKAGING_DIR/lib.sh" 2>&1)"
    STATUS=$?
    CALLS="$(wc -l <"$TMP/calls" | tr -d ' ')"
}

expect() { # DESCRIPTION COMMAND...
    local description=$1
    shift
    if "$@"; then
        printf 'ok    %s\n' "$description"
    else
        printf 'FAIL  %s (status %s, %s codesign calls, output: %s)\n' "$description" "$STATUS" "$CALLS" "$OUTPUT"
        FAILURES=$((FAILURES + 1))
    fi
}

sign ok
expect "a normal signature succeeds with one timestamped call" test "$STATUS" -eq 0 -a "$CALLS" -eq 1 && grep -q -- '--timestamp ' "$TMP/calls"

sign timestamp-server-down
expect "an unreachable timestamp server is an error, not an untimestamped signature" test "$STATUS" -ne 0 -a "$CALLS" -eq 1
expect "the error names the way to allow it" grep -q 'PLAITWAY_ALLOW_NO_TIMESTAMP' <<<"$OUTPUT"

PLAITWAY_ALLOW_NO_TIMESTAMP=1 sign timestamp-server-down
expect "PLAITWAY_ALLOW_NO_TIMESTAMP=1 falls back to no timestamp" test "$STATUS" -eq 0 -a "$CALLS" -eq 2
expect "the retry is the one without a timestamp" grep -q -- '--timestamp=none' "$TMP/calls"

sign other-failure
expect "another failure is reported and not retried" test "$STATUS" -ne 0 -a "$CALLS" -eq 1

started="$(date +%s)"
sign hang
elapsed=$(($(date +%s) - started))
expect "a hung codesign is ended by the watchdog" test "$STATUS" -ne 0 -a "$elapsed" -lt 10
expect "the timeout is named in the error" grep -q 'timed out' <<<"$OUTPUT"
expect "a hung codesign is not retried" test "$CALLS" -eq 1

sign ok -
expect "ad hoc signing never asks for a timestamp" grep -q -- '--timestamp=none' "$TMP/calls"


# make-dist.sh on a small app that is signed ad hoc and carries an extended
# attribute on its files, as every file of a build made on a host that tags
# its output does. Every extractor has to produce an app whose signature holds.
not() { ! "$@"; }
# The listing is captured first: grep -q would SIGPIPE unzip under pipefail.
zip_has_appledouble() {
    local names
    names="$(unzip -Z1 "$1")" || return 0
    grep -q -e '/\._' -e '^__MACOSX' <<<"$names"
}
unzip_then_verify() {
    local out="$TMP/unzipped"
    rm -rf "$out"
    /usr/bin/unzip -q "$1" -d "$out" && codesign --verify --deep --strict "$out/Plaitway.app"
}
BUILD="$TMP/build"
APP="$BUILD/Plaitway.app"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
printf 'int main(void) { return 0; }\n' | cc -x c - -o "$APP/Contents/MacOS/Plaitway"
printf 'notes\n' >"$APP/Contents/Resources/NOTES.txt"
cat >"$APP/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>Plaitway</string>
<key>CFBundleIdentifier</key><string>io.example.test</string>
<key>CFBundlePackageType</key><string>APPL</string>
</dict></plist>
PLIST
xattr -w com.example.attribute 1 "$APP/Contents/MacOS/Plaitway" "$APP/Contents/Resources/NOTES.txt"
codesign --force --sign - --timestamp=none "$APP" 2>/dev/null
OUTPUT="$(PLAITWAY_BUILD_DIR="$BUILD" PLAITWAY_SIGN_IDENTITY=- "$PACKAGING_DIR/make-dist.sh" 2>&1)"
STATUS=$?
CALLS=0
ZIP="$BUILD/Plaitway-$(tr -d '[:space:]' <"$ROOT/VERSION").zip"
expect "make-dist.sh builds a zip and a dmg" test "$STATUS" -eq 0 -a -f "$ZIP" -a -f "${ZIP%.zip}.dmg"
expect "the zip has no AppleDouble entries" not zip_has_appledouble "$ZIP"
expect "the app extracted with unzip passes codesign --verify --deep --strict" unzip_then_verify "$ZIP"

# check_zip: runs check_zip_matches_app from lib.sh on ZIP and APP.
check_zip() {
    OUTPUT="$(bash -c 'source "$1"; check_zip_matches_app "$2" "$3"' _ "$PACKAGING_DIR/lib.sh" "$1" "$APP" 2>&1)"
    STATUS=$?
}
check_zip "$ZIP"
expect "the zip check accepts the zip that make-dist.sh made" test "$STATUS" -eq 0

ditto -c -k --keepParent "$APP" "$TMP/plain.zip"
check_zip "$TMP/plain.zip"
expect "the zip check rejects a zip made without --norsrc" test "$STATUS" -ne 0 && expect "the rejection names AppleDouble" grep -q AppleDouble <<<"$OUTPUT"

# The zip of an earlier build next to a newer app, as after `make app` without `make dist`.
printf 'changed\n' >"$APP/Contents/Resources/NOTES.txt"
codesign --force --sign - --timestamp=none "$APP" 2>/dev/null
check_zip "$ZIP"
expect "the zip check rejects the zip of an older build of the app" test "$STATUS" -ne 0 && expect "the rejection says it is a different build" grep -q 'different build' <<<"$OUTPUT"

# budget_fits DAEMON_GO: the shutdown budget in DAEMON_GO (serverStopTimeout
# plus shutdownTimeout, whole seconds) is below the launchd ExitTimeOut.
budget_fits() {
    local total=0 name seconds
    for name in serverStopTimeout shutdownTimeout; do
        seconds="$(sed -nE "s/^[[:space:]]*${name}[[:space:]]*=[[:space:]]*([0-9]+)[[:space:]]*\*[[:space:]]*time\.Second.*/\1/p" "$1")"
        [ -n "$seconds" ] || { echo "cannot read $name in $1"; return 2; }
        total=$((total + seconds))
    done
    EXIT_TIMEOUT="$(bash -c 'source "$1"; echo "$DAEMON_EXIT_TIMEOUT"' _ "$PACKAGING_DIR/lib.sh")"
    [ "$total" -lt "$EXIT_TIMEOUT" ] || { echo "shutdown budget ${total}s is not below ExitTimeOut ${EXIT_TIMEOUT}s"; return 1; }
}
budget_status() { OUTPUT="$(budget_fits "$1")"; STATUS=$?; }

budget_status "$ROOT/cmd/plaitwayd/daemon.go"
expect "the daemon's shutdown budget is below the ExitTimeOut of its launchd job" test "$STATUS" -eq 0
printf 'const (\n\tserverStopTimeout = 2 * time.Second\n\tshutdownTimeout = 30 * time.Second\n)\n' >"$TMP/slow.go"
budget_status "$TMP/slow.go"
expect "a shutdown budget above the ExitTimeOut is reported" test "$STATUS" -eq 1
printf 'const shutdownTimeout = time.Minute\n' >"$TMP/odd.go"
budget_status "$TMP/odd.go"
expect "a budget that cannot be read is reported, not passed" test "$STATUS" -eq 2

# The launchd helpers of the dev install scripts, against a fake launchctl
# whose job is described by files in $JOB: "loaded" holds the plist the job
# was loaded from, "running" exists while it runs. The output has the layout of
# the real `launchctl print system/<label>`.
JOB="$TMP/job"
mkdir -p "$JOB"
cat >"$TMP/bin/launchctl" <<'FAKE'
#!/bin/bash
[ "$1" = print ] || exit 64
[ -e "$FAKE_JOB/loaded" ] || { echo "Could not find service in domain for system" >&2; exit 113; }
state="not running"
[ -e "$FAKE_JOB/running" ] && state=running
printf '%s = {\n\tactive count = 1\n\tpath = %s\n\ttype = LaunchDaemon\n\tstate = %s\n\n\tprogram = /x\n}\n' "$2" "$(cat "$FAKE_JOB/loaded")" "$state"
FAKE
chmod +x "$TMP/bin/launchctl"
perl -e 'use Socket; socket(S, PF_UNIX, SOCK_STREAM, 0); bind(S, sockaddr_un($ARGV[0])) or die "bind: $!"; sleep 30' "$TMP/sock" &
SOCKET_HOLDER=$!
trap 'kill "$SOCKET_HOLDER" 2>/dev/null; wait "$SOCKET_HOLDER" 2>/dev/null; rm -rf "$TMP"' EXIT
sleep 0.3

# helpers SNIPPET: runs SNIPPET in a shell that has lib.sh and the fake launchctl.
helpers() {
    OUTPUT="$(FAKE_JOB="$JOB" PATH="$TMP/bin:$PATH" PLAITWAY_POLL_INTERVAL=0.01 \
        bash -c 'source "$1"; SOCKET_PATH="$2"; eval "$3"' _ "$PACKAGING_DIR/lib.sh" "$TMP/sock" "$1" 2>&1)"
    STATUS=$?
}
loaded_from() { rm -f "$JOB/running"; printf '%s\n' "$1" >"$JOB/loaded"; }

rm -f "$JOB/loaded" "$JOB/running"
helpers 'job_loaded'
expect "job_loaded is false for a job launchd does not have" test "$STATUS" -ne 0
helpers 'job_source_path'
expect "job_source_path is empty for a job launchd does not have" test -z "$OUTPUT"

loaded_from /Library/LaunchDaemons/legacy.plist
helpers 'job_loaded'
expect "job_loaded is true for a loaded job" test "$STATUS" -eq 0
helpers 'job_source_path'
expect "job_source_path names the plist the job was loaded from" test "$OUTPUT" = /Library/LaunchDaemons/legacy.plist

helpers 'refuse_foreign_job "do it in the app"'
expect "a job loaded from another plist is refused" test "$STATUS" -ne 0
expect "the refusal names the plist it is loaded from" grep -q 'legacy.plist' <<<"$OUTPUT"
expect "the refusal says what to do instead" grep -q 'do it in the app' <<<"$OUTPUT"
loaded_from /Library/LaunchDaemons/io.github.koukeneko.plaitway.daemon.plist
helpers 'refuse_foreign_job "do it in the app"'
expect "a job loaded from the script's own plist is not refused" test "$STATUS" -eq 0
rm -f "$JOB/loaded"
helpers 'refuse_foreign_job "do it in the app"'
expect "no loaded job is not refused" test "$STATUS" -eq 0

loaded_from /Library/LaunchDaemons/io.github.koukeneko.plaitway.daemon.plist
helpers 'helper_running'
expect "a loaded job that is not running is not a running helper" test "$STATUS" -ne 0
touch "$JOB/running"
helpers 'helper_running'
expect "a running job with a socket is a running helper" test "$STATUS" -eq 0
helpers 'SOCKET_PATH=/nonexistent/sock; helper_running'
expect "a running job without its socket is not a running helper" test "$STATUS" -ne 0
rm -f "$JOB/loaded" "$JOB/running"
helpers 'helper_running'
expect "no job is not a running helper" test "$STATUS" -ne 0

# shellcheck disable=SC2016 # the snippets are code for helpers, not expansions here
helpers 'n=0; third() { n=$((n + 1)); [ "$n" -ge 3 ]; }; wait_until 5 third'
expect "wait_until returns once the command succeeds" test "$STATUS" -eq 0
# shellcheck disable=SC2016
helpers 'n=0; never() { n=$((n + 1)); [ "$n" -ge 100 ]; }; wait_until 2 never'
expect "wait_until gives up after its seconds" test "$STATUS" -eq 1

# build_path_leaks and binary_has_string read the strings of a file.
printf 'int main(void) { return 0; }\n' | cc -x c - -o "$TMP/clean"
printf '\0\0plain text\0/Users/someone/work/openssl/crypto/x.c\0\0/opt/work/dir/y.c\0' >"$TMP/leaky"
leaks() { OUTPUT="$(bash -c 'source "$1"; shift; build_path_leaks "$@"' _ "$PACKAGING_DIR/lib.sh" "$@" 2>&1)"; STATUS=$?; }
leaks "$TMP/clean"
expect "a binary without a build path has no leaks" test "$STATUS" -eq 0 -a -z "$OUTPUT"
leaks "$TMP/leaky"
expect "a home directory in a binary is a leak" test "$STATUS" -eq 0 -a "$OUTPUT" = /Users/someone/work/openssl/crypto/x.c
leaks "$TMP/leaky" /opt/work
expect "a directory passed as an argument is a leak too" test "$(wc -l <<<"$OUTPUT" | tr -d ' ')" -eq 2
leaks "$TMP/does-not-exist"
expect "a file that cannot be read is an error, not a clean result" test "$STATUS" -eq 2
has_string() { bash -c 'source "$1"; binary_has_string "$2" "$3"' _ "$PACKAGING_DIR/lib.sh" "$@" >/dev/null 2>&1; }
expect "binary_has_string finds a string" has_string "$TMP/leaky" plain
expect "binary_has_string does not find a missing string" not has_string "$TMP/leaky" missing

[ "$FAILURES" -eq 0 ]
