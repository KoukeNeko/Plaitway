# shellcheck shell=bash
# shellcheck disable=SC2034 # the variables are used by the scripts that source this
# Shared helpers for packaging scripts. Source it; do not execute it.
# Targets bash 3.2 (the macOS /bin/bash).

PACKAGING_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$PACKAGING_DIR/.." && pwd)"
# PLAITWAY_BUILD_DIR moves every build product, for the tests of these scripts.
BUILD_DIR="${PLAITWAY_BUILD_DIR:-$ROOT/build}"

# Names shared by the packaging and install scripts. They must match the
# daemon's own defaults and the app's DaemonLocation.
BUNDLE_ID=io.github.koukeneko.plaitway
DAEMON_LABEL=io.github.koukeneko.plaitway.daemon
RUN_DIR=/var/run/plaitway
SOCKET_PATH=$RUN_DIR/plaitwayd.sock
STATE_DIR="/Library/Application Support/Plaitway"
LOG_DIR=/Library/Logs/Plaitway
LOG_FILE=$LOG_DIR/plaitwayd.log
MIN_MACOS=15.0

# Code-signing identifiers of the bundled executables; the others are
# $BUNDLE_ID (the app) and $DAEMON_LABEL (the daemon).
OPENVPN_IDENTIFIER=$BUNDLE_ID.openvpn
CLI_IDENTIFIER=$BUNDLE_ID.cli

# ExitTimeOut of the daemon's launchd job, in seconds. launchd sends SIGKILL
# when the daemon has not exited that long after SIGTERM, and where the key is
# unset it waits only 5 s. The daemon needs its whole shutdown budget
# (serverStopTimeout and shutdownTimeout in cmd/plaitwayd/daemon.go) to remove
# its routes and DNS entries, so that budget must stay below this value;
# packaging/lib_test.sh checks it.
DAEMON_EXIT_TIMEOUT=20

# Where the dev-install scripts keep the daemon when SMAppService approval is
# not available. A root-owned copy: a root daemon must never run from a
# location the user can write.
DEV_APP_DIR=/Library/PrivilegedHelperTools/Plaitway.app
DEV_PLIST=/Library/LaunchDaemons/$DAEMON_LABEL.plist

# Two certificates share the Developer ID name, so the default is the SHA-1.
# "-" signs ad hoc (no certificate, no timestamp), for machines without one.
SIGN_IDENTITY="${PLAITWAY_SIGN_IDENTITY:-86A98634D7596D78A0F6BC838B99FF93AFE5BD45}"
SIGN_TIMEOUT_SECONDS=60

# notary_credentials sets NOTARY_AUTH, the notarytool arguments that say who is submitting: an
# App Store Connect API key (PLAITWAY_NOTARY_KEY, the .p8 file, with PLAITWAY_NOTARY_KEY_ID and
# PLAITWAY_NOTARY_ISSUER), which is what a build machine without a keychain profile uses, or else
# the keychain profile PLAITWAY_NOTARY_PROFILE (default plaitway-notary).
notary_credentials() {
    if [ -n "${PLAITWAY_NOTARY_KEY:-}" ]; then
        [ -f "$PLAITWAY_NOTARY_KEY" ] || die "PLAITWAY_NOTARY_KEY is not a file: $PLAITWAY_NOTARY_KEY"
        [ -n "${PLAITWAY_NOTARY_KEY_ID:-}" ] && [ -n "${PLAITWAY_NOTARY_ISSUER:-}" ] ||
            die "PLAITWAY_NOTARY_KEY needs PLAITWAY_NOTARY_KEY_ID and PLAITWAY_NOTARY_ISSUER"
        NOTARY_AUTH=(--key "$PLAITWAY_NOTARY_KEY" --key-id "$PLAITWAY_NOTARY_KEY_ID" --issuer "$PLAITWAY_NOTARY_ISSUER")
    else
        NOTARY_AUTH=(--keychain-profile "${PLAITWAY_NOTARY_PROFILE:-plaitway-notary}")
    fi
}

# read_version sets VERSION from the VERSION file.
read_version() {
    VERSION="$(tr -d '[:space:]' <"$ROOT/VERSION")"
    [[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "VERSION must look like 1.2.3, got '$VERSION'"
}

# confirm ASSUME_YES asks on the terminal before a system change, unless
# ASSUME_YES is 1.
confirm() {
    [ "$1" = 1 ] && return 0
    { : </dev/tty; } 2>/dev/null || die "no terminal to ask on; pass --yes to proceed"
    local answer
    printf 'Proceed? [y/N] ' >/dev/tty
    read -r answer </dev/tty
    case "$answer" in y | Y | yes) ;; *) die "aborted, nothing was changed" ;; esac
}

log() { printf '==> %s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

# run_with_timeout SECONDS COMMAND...: kills COMMAND after SECONDS and returns
# 124. A keychain prompt makes codesign wait forever with nobody to answer it.
run_with_timeout() {
    local seconds=$1 pid watchdog fired status=0
    shift
    fired="$(mktemp -u)"
    "$@" &
    pid=$!
    (
        sleep "$seconds" &
        sleeper=$!
        trap 'kill "$sleeper" 2>/dev/null; exit 0' TERM
        wait "$sleeper"
        : >"$fired"
        kill -TERM "$pid" 2>/dev/null
        exit 0
    ) &
    watchdog=$!
    { wait "$pid"; } 2>/dev/null || status=$?
    kill -TERM "$watchdog" 2>/dev/null
    wait "$watchdog" 2>/dev/null || true
    if [ -e "$fired" ]; then
        rm -f "$fired"
        return 124
    fi
    return "$status"
}

# run_codesign TARGET CODESIGN_ARGS...: signs TARGET with the configured
# identity and a secure timestamp under the watchdog. Notarization rejects a
# signature without a timestamp, so an unreachable timestamp server is an
# error, unless PLAITWAY_ALLOW_NO_TIMESTAMP=1 asks for the untimestamped
# signature of a build that will not be notarized.
run_codesign() {
    local target=$1 out status=0
    shift
    out="$(mktemp)"
    if [ "$SIGN_IDENTITY" = "-" ]; then
        run_with_timeout "$SIGN_TIMEOUT_SECONDS" codesign --force --sign - --timestamp=none "$@" "$target" >"$out" 2>&1 || status=$?
    else
        run_with_timeout "$SIGN_TIMEOUT_SECONDS" codesign --force --sign "$SIGN_IDENTITY" --timestamp "$@" "$target" >"$out" 2>&1 || status=$?
        if [ "$status" -ne 0 ] && [ "$status" -ne 124 ] && grep -qi 'timestamp' "$out"; then
            if [ "${PLAITWAY_ALLOW_NO_TIMESTAMP:-0}" = 1 ]; then
                log "timestamp server unreachable, signing $(basename "$target") without a timestamp"
                status=0
                run_with_timeout "$SIGN_TIMEOUT_SECONDS" codesign --force --sign "$SIGN_IDENTITY" --timestamp=none "$@" "$target" >"$out" 2>&1 || status=$?
            else
                cat "$out" >&2
                rm -f "$out"
                die "the timestamp server is unreachable, so $target cannot be signed for notarization; retry, or set PLAITWAY_ALLOW_NO_TIMESTAMP=1 to sign without a timestamp"
            fi
        fi
    fi
    if [ "$status" -ne 0 ]; then
        cat "$out" >&2
        rm -f "$out"
        if [ "$status" -eq 124 ]; then
            die "codesign timed out after ${SIGN_TIMEOUT_SECONDS}s on $target (a keychain prompt is likely waiting)"
        fi
        die "codesign failed on $target"
    fi
    rm -f "$out"
}

# sign_path PATH IDENTIFIER: hardened-runtime signature for a Mach-O or an app
# bundle. Passing IDENTIFIER keeps the code-signing identifier stable instead
# of derived from the file name.
sign_path() { run_codesign "$1" --identifier "$2" --options runtime; }

# sign_disk_image PATH: disk images take a plain signature, no runtime flag.
sign_disk_image() { run_codesign "$1" --identifier "$BUNDLE_ID.dmg"; }

# cdhash_of PATH: the CDHash of PATH's code signature, which identifies the
# exact build.
cdhash_of() { codesign -dvvv "$1" 2>&1 | sed -n 's/^CDHash=//p'; }

# check_zip_matches_app ZIP APP: ZIP lists no AppleDouble entry, unpacks with
# /usr/bin/unzip into an app whose signature verifies, and that app is the
# build of APP (same CDHash). Prints the problem and returns 1 otherwise.
check_zip_matches_app() {
    local zip=$1 app=$2 names unpacked status=0 want got
    # Captured first: grep -q exiting early would SIGPIPE unzip under pipefail.
    names="$(unzip -Z1 "$zip")" || { echo "cannot list $zip"; return 1; }
    if grep -q -e '/\._' -e '^__MACOSX' <<<"$names"; then
        echo "$zip holds AppleDouble entries: unzip would write them into the bundle and break its signature"
        return 1
    fi
    unpacked="$(mktemp -d)"
    /usr/bin/unzip -q "$zip" -d "$unpacked" || status=1
    if [ "$status" -eq 0 ] && ! codesign --verify --deep --strict "$unpacked/$(basename "$app")" 2>&1; then
        echo "the app unpacked from $zip with unzip fails codesign --verify"
        status=1
    fi
    if [ "$status" -eq 0 ]; then
        want="$(cdhash_of "$app")"
        got="$(cdhash_of "$unpacked/$(basename "$app")")"
        if [ -z "$want" ] || [ "$got" != "$want" ]; then
            echo "$zip holds a different build than $app (CDHash '$got', want '$want')"
            status=1
        fi
    fi
    rm -rf "$unpacked"
    return "$status"
}

# build_path_leaks FILE [PATH...]: prints the strings of FILE that name a
# directory of the machine that built it (the usual scratch and home
# locations, and any PATH given). Nothing printed is the good outcome; the
# status is 2 when FILE cannot be scanned.
build_path_leaks() {
    local file=$1 listing path patterns=(-e /private/tmp -e /Users/ -e /var/folders)
    shift
    for path in "$@"; do
        patterns[${#patterns[@]}]=-e
        patterns[${#patterns[@]}]="$path"
    done
    listing="$(strings -a "$file")" || return 2
    grep -F "${patterns[@]}" <<<"$listing" || true
}

# binary_has_string FILE TEXT: FILE contains the string TEXT. The listing is
# captured first: grep -q exiting early would SIGPIPE strings under pipefail.
binary_has_string() {
    local listing
    listing="$(strings -a "$1")" || return 2
    grep -qF -- "$2" <<<"$listing"
}

# Helpers of the scripts that install the daemon as a plain LaunchDaemon.

# Seconds between polls of wait_until; the tests of these helpers shorten it.
POLL_INTERVAL="${PLAITWAY_POLL_INTERVAL:-1}"

# wait_until SECONDS COMMAND...: runs COMMAND once per poll, for up to SECONDS
# seconds, until it succeeds.
wait_until() {
    local seconds=$1 waited=0
    shift
    until "$@"; do
        [ "$waited" -lt "$seconds" ] || return 1
        sleep "$POLL_INTERVAL"
        waited=$((waited + 1))
    done
}

# job_loaded: launchd has the daemon's job. It says nothing about who loaded it.
job_loaded() { launchctl print "system/$DAEMON_LABEL" >/dev/null 2>&1; }

job_gone() { ! job_loaded; }

# job_source_path: the plist launchd loaded the job from, so that a job that an
# SMAppService registration owns is not fought over. Empty when not loaded.
job_source_path() {
    launchctl print "system/$DAEMON_LABEL" 2>/dev/null | awk -F' = ' '$1 ~ /^[[:space:]]*path$/ && !seen { print $2; seen = 1 }'
}

# helper_running: the job is running and its socket exists. The output of
# launchctl is captured first: grep -q exiting early would SIGPIPE it.
helper_running() {
    local job
    [ -S "$SOCKET_PATH" ] || return 1
    job="$(launchctl print "system/$DAEMON_LABEL" 2>/dev/null)" || return 1
    grep -q 'state = running' <<<"$job"
}

# refuse_foreign_job: dies when the job is loaded from somewhere other than the
# plist of the plain LaunchDaemon install, which is how an SMAppService
# registration looks. $1 is what to do instead.
refuse_foreign_job() {
    local loaded_from
    job_loaded || return 0
    loaded_from="$(job_source_path)"
    if [ -n "$loaded_from" ] && [ "$loaded_from" != "$DEV_PLIST" ]; then
        die "$DAEMON_LABEL is loaded from $loaded_from, not from this script's install: $1"
    fi
}
