#!/bin/bash
# Assembles and signs build/Plaitway.app: the Swift app, the Go daemon and
# command line client, the bundled OpenVPN with its source code and build
# scripts, the LaunchDaemon plist for SMAppService, the icon and the
# third-party notices. Run `make openvpn` first (the Makefile target does).
#
# PLAITWAY_SIGN_IDENTITY selects the signing identity (default: the Developer
# ID certificate by SHA-1; "-" signs ad hoc). Order matters: the daemon is
# built with the SHA-256 of the signed openvpn in the bundle baked in, so
# openvpn is signed first and nothing touches its bytes afterwards.
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
# shellcheck source=openvpn-deps.env
source "$PACKAGING_DIR/openvpn-deps.env"

COPYRIGHT="Copyright © 2026 KoukeNeko"

read_version

APP="$BUILD_DIR/Plaitway.app"
STAGE="$BUILD_DIR/stage"
OPENVPN="$BUILD_DIR/openvpn/bin/openvpn"
OPENVPN_SOURCES="${PLAITWAY_OPENVPN_WORK:-$BUILD_DIR/openvpn-work}/src"
SWIFT_SCRATCH="$BUILD_DIR/swift"
# The GPL archives and the libraries linked statically into openvpn. OpenSSL
# (Apache-2.0, 53 MB) stays a URL in the notices. packaging/notices says the
# same list.
SHIPPED_SOURCE_URLS=("$OPENVPN_URL" "$LZO_URL" "$LZ4_URL")

[ -x "$OPENVPN" ] || die "$OPENVPN is missing; run packaging/build-openvpn.sh (make openvpn)"

# build_go PACKAGE OUTPUT [LDFLAGS...]: a Go executable for the app.
build_go() {
    local package=$1 output=$2
    shift 2
    log "building $package $VERSION"
    (
        cd "$ROOT"
        export GOOS=darwin GOARCH=arm64
        export CGO_CFLAGS="-mmacosx-version-min=$MIN_MACOS" CGO_LDFLAGS="-mmacosx-version-min=$MIN_MACOS"
        go build -trimpath -buildvcs=false -ldflags "-s -w -X main.version=$VERSION $*" -o "$output" "$package"
    )
}

rm -rf "$STAGE"
mkdir -p "$STAGE"

# A separate scratch path keeps this build from contending for macos/.build
# with a developer's own swift build. The rpath lets the executable find the
# Swift libraries embedded in Contents/Frameworks. The macro prefix map keeps
# the build machine's paths out of the __FILE__ strings of the BoringSSL
# assertions that swift-nio-ssl and swift-crypto compile in.
SWIFT_BUILD=(swift build -c release --package-path "$ROOT/macos" --scratch-path "$SWIFT_SCRATCH"
    -Xlinker -rpath -Xlinker @executable_path/../Frameworks
    -Xcc "-fmacro-prefix-map=$ROOT=.")
log "building the Swift app (release)"
"${SWIFT_BUILD[@]}"
SWIFT_BIN_DIR="$("${SWIFT_BUILD[@]}" --show-bin-path)"
APP_EXECUTABLE=""
for name in Plaitway PlaitwayMenuBar; do
    if [ -x "$SWIFT_BIN_DIR/$name" ]; then
        APP_EXECUTABLE="$SWIFT_BIN_DIR/$name"
        break
    fi
done
[ -n "$APP_EXECUTABLE" ] || die "no Plaitway or PlaitwayMenuBar executable in $SWIFT_BIN_DIR"

log "drawing the icon"
swift "$PACKAGING_DIR/make-icon.swift" "$STAGE/Plaitway.iconset"
iconutil --convert icns "$STAGE/Plaitway.iconset" --output "$STAGE/Plaitway.icns"

log "generating third-party notices"
(
    cd "$ROOT"
    go run ./packaging/notices -sources "$OPENVPN_SOURCES" -checkouts "$SWIFT_SCRATCH/checkouts" \
        -o "$PACKAGING_DIR/THIRD_PARTY_NOTICES.md"
)

log "assembling $APP"
# A zip or dmg made from an earlier app must not outlive it: they carry the
# same version number, so nothing else would tell them apart.
rm -rf "$APP"
rm -f "$BUILD_DIR"/Plaitway-*.zip "$BUILD_DIR"/Plaitway-*.dmg
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources/bin" "$APP/Contents/Library/LaunchDaemons"
cp "$APP_EXECUTABLE" "$APP/Contents/MacOS/Plaitway"
# Swift 6.2 binaries that target macOS before 26 link a back-deployment
# library the OS does not ship; this copies whatever the executable needs.
xcrun swift-stdlib-tool --copy --scan-executable "$APP/Contents/MacOS/Plaitway" \
    --platform macosx --destination "$APP/Contents/Frameworks"
for library in "$APP"/Contents/Frameworks/*.dylib; do
    [ -e "$library" ] || continue
    # The toolchain's copy is universal; the app is arm64 only.
    if [ "$(lipo -archs "$library")" != arm64 ]; then
        lipo -thin arm64 "$library" -output "$library.arm64"
        mv "$library.arm64" "$library"
    fi
done
cp "$OPENVPN" "$APP/Contents/Resources/bin/openvpn"
cp "$STAGE/Plaitway.icns" "$APP/Contents/Resources/Plaitway.icns"
cp "$PACKAGING_DIR/THIRD_PARTY_NOTICES.md" "$APP/Contents/Resources/THIRD_PARTY_NOTICES.md"

# The corresponding source of the GPL parts of openvpn travels with the
# binary (GPLv2 section 3(a)), together with the scripts that build it.
mkdir -p "$APP/Contents/Resources/Source"
for url in "${SHIPPED_SOURCE_URLS[@]}"; do
    archive="$OPENVPN_SOURCES/$(basename "$url")"
    [ -f "$archive" ] || die "$archive is missing; run packaging/build-openvpn.sh (make openvpn)"
    cp "$archive" "$APP/Contents/Resources/Source/"
done
cp "$PACKAGING_DIR/build-openvpn.sh" "$PACKAGING_DIR/lib.sh" "$PACKAGING_DIR/openvpn-deps.env" \
    "$APP/Contents/Resources/Source/"

# The standard About panel shows Credits.rtf from the main bundle.
cat >"$APP/Contents/Resources/Credits.rtf" <<'EOF'
{\rtf1\ansi\deff0{\fonttbl{\f0\fswiss Helvetica;}}\f0\fs18
Plaitway includes OpenVPN, LZO, LZ4, OpenSSL and wireguard-go, among other open source software.\par
\par
The source code of OpenVPN and LZO and the scripts that build them are in Contents/Resources/Source of this app. Licenses, the written offer for the source code and trademark notices are in Contents/Resources/THIRD_PARTY_NOTICES.md.\par
}
EOF
# SwiftPM resource bundles sit next to the executable in the build directory.
for bundle in "$SWIFT_BIN_DIR"/*.bundle; do
    [ -e "$bundle" ] || continue
    ditto "$bundle" "$APP/Contents/Resources/$(basename "$bundle")"
done

cat >"$APP/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleDevelopmentRegion</key>
	<string>en</string>
	<key>CFBundleExecutable</key>
	<string>Plaitway</string>
	<key>CFBundleIconFile</key>
	<string>Plaitway</string>
	<key>CFBundleIdentifier</key>
	<string>$BUNDLE_ID</string>
	<key>CFBundleInfoDictionaryVersion</key>
	<string>6.0</string>
	<key>CFBundleLocalizations</key>
	<array>
		<string>en</string>
		<string>zh-Hant</string>
	</array>
	<key>CFBundleName</key>
	<string>Plaitway</string>
	<key>CFBundleDocumentTypes</key>
	<array>
		<dict>
			<key>CFBundleTypeName</key>
			<string>VPN Profile</string>
			<key>CFBundleTypeRole</key>
			<string>Viewer</string>
			<key>LSHandlerRank</key>
			<string>Alternate</string>
			<key>CFBundleTypeExtensions</key>
			<array>
				<string>ovpn</string>
				<string>conf</string>
			</array>
		</dict>
	</array>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>$VERSION</string>
	<key>CFBundleVersion</key>
	<string>$VERSION</string>
	<key>LSApplicationCategoryType</key>
	<string>public.app-category.utilities</string>
	<key>LSMinimumSystemVersion</key>
	<string>$MIN_MACOS</string>
	<key>LSUIElement</key>
	<true/>
	<key>NSHighResolutionCapable</key>
	<true/>
	<key>NSHumanReadableCopyright</key>
	<string>$COPYRIGHT</string>
</dict>
</plist>
EOF

# BundleProgram is relative to the bundle; SMAppService registers this plist
# and launchd runs the daemon as root. There is no StandardErrorPath: launchd
# fails a job whose log directory is missing and nothing creates one before
# the first start, so the daemon makes $LOG_DIR and logs there itself.
# ExitTimeOut: see DAEMON_EXIT_TIMEOUT in lib.sh.
cat >"$APP/Contents/Library/LaunchDaemons/$DAEMON_LABEL.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>$DAEMON_LABEL</string>
	<key>BundleProgram</key>
	<string>Contents/MacOS/plaitwayd</string>
	<key>ProgramArguments</key>
	<array>
		<string>plaitwayd</string>
		<string>-socket</string>
		<string>$SOCKET_PATH</string>
		<string>-socket-mode</string>
		<string>0666</string>
		<string>-state-dir</string>
		<string>$STATE_DIR</string>
		<string>-run-dir</string>
		<string>$RUN_DIR</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ExitTimeOut</key>
	<integer>$DAEMON_EXIT_TIMEOUT</integer>
	<key>AssociatedBundleIdentifiers</key>
	<array>
		<string>$BUNDLE_ID</string>
	</array>
</dict>
</plist>
EOF
plutil -lint "$APP/Contents/Info.plist" "$APP/Contents/Library/LaunchDaemons/$DAEMON_LABEL.plist"

# Build products can carry extended attributes (quarantine, provenance) that
# codesign rejects as detritus.
xattr -cr "$APP"

log "signing with ${SIGN_IDENTITY}"
# Nested code first, then the bundle that seals it. Neither the app nor the
# daemon needs a hardened-runtime exception, so there are no entitlements.
for library in "$APP"/Contents/Frameworks/*.dylib; do
    [ -e "$library" ] || continue
    sign_path "$library" "$BUNDLE_ID.$(basename "$library" .dylib)"
done
sign_path "$APP/Contents/Resources/bin/openvpn" "$OPENVPN_IDENTIFIER"
# The hash of the signed bytes: the daemon runs only an openvpn with this hash.
OPENVPN_SHA256="$(shasum -a 256 "$APP/Contents/Resources/bin/openvpn" | cut -d' ' -f1)"
log "openvpn sha256 $OPENVPN_SHA256"

build_go ./cmd/plaitwayd "$APP/Contents/MacOS/plaitwayd" "-X main.openvpnSHA256=$OPENVPN_SHA256"
build_go ./cmd/plaitway "$APP/Contents/Resources/bin/plaitway"
sign_path "$APP/Contents/MacOS/plaitwayd" "$DAEMON_LABEL"
sign_path "$APP/Contents/Resources/bin/plaitway" "$CLI_IDENTIFIER"
sign_path "$APP" "$BUNDLE_ID"

log "verifying the signature"
codesign --verify --deep --strict --verbose=2 "$APP"
# An unnotarized app is rejected here; notarize.sh is what changes that.
spctl --assess --type execute --verbose=4 "$APP" || log "spctl rejects the app, expected until it is notarized"

log "done: $APP"
