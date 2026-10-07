#!/bin/bash
# Builds the bundled OpenVPN: arm64, macOS 15+, OpenSSL/LZO/LZ4 linked
# statically, signed with Developer ID. Output: build/openvpn/bin/openvpn.
#
# Sources and checksums come from openvpn-deps.env. Intermediates live in
# $PLAITWAY_OPENVPN_WORK (default build/openvpn-work); downloads are cached
# there and re-verified on every run. The result does not depend on where that
# directory is: nothing of it is baked into the binary (checked below).
#
# This script, lib.sh and openvpn-deps.env ship in the app bundle next to the
# source archives: they are the "scripts used to control compilation" that
# GPLv2 section 3 counts as part of the source.
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
# shellcheck source=openvpn-deps.env
source "$PACKAGING_DIR/openvpn-deps.env"

WORK="${PLAITWAY_OPENVPN_WORK:-$BUILD_DIR/openvpn-work}"
SRC="$WORK/src"
OUT="$BUILD_DIR/openvpn"
STAGE="$WORK/openvpn-stage"
JOBS="$(sysctl -n hw.ncpu)"
# Every library and openvpn itself is configured for this prefix, and the
# libraries are installed into $DEPS (DESTDIR), so the work directory is never
# the prefix. The binaries bake the prefix in as the places they would look
# for helper scripts, plug-ins and OpenSSL modules; /var/empty is root-owned
# and nobody can create anything in it, so there is nothing to find there.
PREFIX=/var/empty
DEPS="$WORK/deps"
LIBROOT="$DEPS$PREFIX"

[ "$(uname -m)" = arm64 ] || die "this script builds arm64 on an arm64 Mac"

export MACOSX_DEPLOYMENT_TARGET="$MIN_MACOS"
export CC="clang"
export CFLAGS="-O2 -arch arm64 -mmacosx-version-min=$MIN_MACOS"
export LDFLAGS="-arch arm64 -mmacosx-version-min=$MIN_MACOS"
# Nothing may be picked up from Homebrew or another prefix.
export PKG_CONFIG_LIBDIR="$WORK/no-pkgconfig"
export PATH="/usr/bin:/bin:/usr/sbin:/sbin"

fetch() { # NAME URL SHA256
    local file actual
    file="$SRC/$(basename "$2")"
    mkdir -p "$SRC"
    if [ ! -f "$file" ]; then
        log "downloading $1"
        curl -fsSL --retry 3 --max-time 300 -o "$file.part" "$2"
        mv "$file.part" "$file"
    fi
    actual="$(shasum -a 256 "$file" | cut -d' ' -f1)"
    [ "$actual" = "$3" ] || die "$1: SHA-256 mismatch (got $actual, want $3); delete $file and retry"
}

# run LOGNAME COMMAND...: output goes to $WORK/logs, shown only on failure.
run() {
    local name=$1
    shift
    mkdir -p "$WORK/logs"
    if ! "$@" >"$WORK/logs/$name.log" 2>&1; then
        tail -n 40 "$WORK/logs/$name.log" >&2
        die "$name failed (full log: $WORK/logs/$name.log)"
    fi
}

extract() { # TARBALL -> $WORK/<dirname>
    rm -rf "${WORK:?}/$(basename "$1" .tar.gz)"
    tar -xzf "$1" -C "$WORK"
}

fetch openvpn "$OPENVPN_URL" "$OPENVPN_SHA256"
fetch openssl "$OPENSSL_URL" "$OPENSSL_SHA256"
fetch lzo "$LZO_URL" "$LZO_SHA256"
fetch lz4 "$LZ4_URL" "$LZ4_SHA256"

rm -rf "$DEPS" "$WORK/no-pkgconfig"
mkdir -p "$DEPS" "$WORK/no-pkgconfig"

log "building OpenSSL $OPENSSL_VERSION"
extract "$SRC/$(basename "$OPENSSL_URL")"
(
    cd "$WORK/openssl-$OPENSSL_VERSION"
    # The daemon runs openvpn as root on untrusted profiles, so OpenSSL gets no
    # way to load code or configuration from the filesystem. no-dso removes
    # loading providers from shared libraries (no-module and no-engine only
    # cover engines), and openssldir and the prefix point at /var/empty, so no
    # openssl.cnf is found and the engine and module directories baked in name
    # nothing a user could create. The hardened runtime's library validation is
    # the independent second layer.
    run openssl-configure ./Configure darwin64-arm64-cc no-shared no-dso no-module no-engine no-tests no-docs \
        --prefix="$PREFIX" --openssldir="$PREFIX"
    run openssl-make make -j"$JOBS" build_libs
    run openssl-install make install_dev DESTDIR="$DEPS"
)

log "building LZO $LZO_VERSION"
extract "$SRC/$(basename "$LZO_URL")"
(
    cd "$WORK/lzo-$LZO_VERSION"
    # lzo 2.10 predates arm64 macOS: its bundled config.guess cannot name this host.
    run lzo-configure ./configure --prefix="$PREFIX" --disable-shared --enable-static \
        --build=aarch64-apple-darwin
    run lzo-make make -j"$JOBS"
    run lzo-install make install DESTDIR="$DEPS"
)

log "building LZ4 $LZ4_VERSION"
extract "$SRC/$(basename "$LZ4_URL")"
run lz4 make -C "$WORK/lz4-$LZ4_VERSION/lib" -j"$JOBS" PREFIX="$PREFIX" DESTDIR="$DEPS" BUILD_SHARED=no install

log "building OpenVPN $OPENVPN_VERSION"
extract "$SRC/$(basename "$OPENVPN_URL")"
rm -rf "$STAGE"
mkdir -p "$STAGE"
(
    cd "$WORK/openvpn-$OPENVPN_VERSION"
    # A profile is untrusted input and openvpn runs as root, so it must not be
    # able to load a dylib or run a helper: --disable-plugins removes the
    # "plugin" directive, and --disable-dns-updown-by-default stops the
    # dns-updown helper from running unless a profile asks (the daemon also
    # passes --dns-updown disable, and the profile filter refuses the
    # directive). SCRIPTDIR keeps the helper's default path out of the work
    # directory; the string stays in the binary but names nothing.
    # $LIBROOT/lib holds only .a files (every dependency above was built
    # without shared libraries), so -L/-l links them statically.
    export OPENSSL_CFLAGS="-I$LIBROOT/include" OPENSSL_LIBS="-L$LIBROOT/lib -lssl -lcrypto"
    export LZO_CFLAGS="-I$LIBROOT/include" LZO_LIBS="-L$LIBROOT/lib -llzo2"
    export LZ4_CFLAGS="-I$LIBROOT/include" LZ4_LIBS="-L$LIBROOT/lib -llz4"
    export SCRIPTDIR="$PREFIX"
    run openvpn-configure ./configure --prefix="$PREFIX" \
        --with-crypto-library=openssl --disable-plugins --disable-dns-updown-by-default \
        --disable-unit-tests --disable-debug
    run openvpn-make make -j"$JOBS"
    # Nothing is installed: the one executable is all that is used.
    cp src/openvpn/openvpn "$STAGE/openvpn"
)

BIN="$STAGE/openvpn"
[ -x "$BIN" ] || die "build finished without producing $BIN"
strip -x "$BIN"

log "checking linkage"
if otool -L "$BIN" | tail -n +2 | awk '{print $1}' | grep -v -e '^/usr/lib/' -e '^/System/Library/'; then
    die "openvpn links against non-system libraries (listed above)"
fi

log "checking for build paths and the dns-updown default"
leaks="$(build_path_leaks "$BIN" "$WORK")" || die "cannot scan $BIN for build paths"
if [ -n "$leaks" ]; then
    printf '%s\n' "$leaks" >&2
    die "openvpn embeds a path of this machine (listed above)"
fi
# The compile-time settings string records the option.
binary_has_string "$BIN" enable_dns_updown_by_default=no ||
    die "openvpn was built with the dns-updown helper enabled by default"

sign_path "$BIN" "$OPENVPN_IDENTIFIER"

log "checking features"
# Output is captured whole: a reader that quits early would SIGPIPE openvpn.
VERSION_OUTPUT="$("$BIN" --version)"
VERSION_OUTPUT="${VERSION_OUTPUT%%$'\n'*}"
log "$VERSION_OUTPUT"
case "$VERSION_OUTPUT" in
    "OpenVPN $OPENVPN_VERSION "*"[LZO]"*"[LZ4]"*) ;;
    *) die "unexpected --version line: $VERSION_OUTPUT" ;;
esac
CIPHERS="$("$BIN" --show-ciphers)"
grep -q '^AES-128-CBC ' <<<"$CIPHERS" || die "AES-128-CBC missing from --show-ciphers"
# Replace by rename, never by rewriting in place: a running copy keeps its
# inode and its signature stays valid.
mkdir -p "$OUT/bin"
cp "$BIN" "$OUT/bin/.openvpn.new"
mv -f "$OUT/bin/.openvpn.new" "$OUT/bin/openvpn"
log "done: $OUT/bin/openvpn"
