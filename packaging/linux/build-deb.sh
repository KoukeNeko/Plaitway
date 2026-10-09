#!/bin/sh
# Builds build/linux/plaitway_<version>_<arch>.deb with dpkg-deb, without
# debhelper: the files come from install.sh, the control file and the
# maintainer scripts from here. The version is the VERSION file. The package is
# built for the architecture of the machine (dpkg --print-architecture): cgo
# needs the C toolchain and the C library of the target, so arm64 is built on
# an arm64 machine (the CI uses GitHub's ubuntu-24.04-arm runner), not
# cross-compiled.
#
#   packaging/linux/build-deb.sh [--out DIR]
#
# Needs dpkg-deb and dpkg-shlibdeps (dpkg-dev), Go and a C compiler.
set -eu
HERE="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib.sh
. "$HERE/lib.sh"

OUT="$BUILD_DIR"
while [ $# -gt 0 ]; do
    case "$1" in
        --out) need_arg "$1" $#; OUT="$2"; shift 2 ;;
        *) die "usage: $0 [--out DIR]" ;;
    esac
done
for tool in dpkg-deb dpkg-shlibdeps gcc go; do
    command -v "$tool" >/dev/null 2>&1 || die "$tool is needed (dpkg-dev, gcc and Go)"
done
read_version

ARCH="$(dpkg --print-architecture)"
case "$ARCH" in
    amd64) GOARCH=amd64 ;;
    arm64) GOARCH=arm64 ;;
    *) die "the architecture $ARCH is not supported; the package is built for amd64 and arm64" ;;
esac
export GOARCH
# The date of every file in the package, and of the changelog entry.
SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-$(git -C "$ROOT" log -1 --format=%ct 2>/dev/null || date +%s)}"
export SOURCE_DATE_EPOCH

mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
sh "$PACKAGING_LINUX/build.sh" --out "$OUT"

# The layout of debhelper (debian/<package>), which dpkg-shlibdeps expects.
WORK="$OUT/deb"
STAGE="$WORK/debian/plaitway"
rm -rf "$WORK"
mkdir -p "$STAGE/DEBIAN"
sh "$PACKAGING_LINUX/install.sh" --prefix /usr --destdir "$STAGE" --build "$OUT" --version "$VERSION"

log "computing the shared library dependencies"
printf 'Source: plaitway\n\nPackage: plaitway\nArchitecture: any\n' >"$WORK/debian/control"
# Both programs link the C library only; dpkg-shlibdeps names the oldest
# release that has every symbol they use.
shlibs="$(cd "$WORK" && dpkg-shlibdeps -O -edebian/plaitway/usr/libexec/plaitway/plaitwayd -edebian/plaitway/usr/bin/plaitway |
    sed -n 's/^shlibs:Depends=//p')"
[ -n "$shlibs" ] || die "dpkg-shlibdeps found no dependency; the programs should link libc6"

# gir1.2-adw-1 1.7 is the oldest libadwaita with Adw.ToggleGroup (the app's page
# switchers); Ubuntu 24.04 has 1.5, so the app needs 25.04 or later or Debian 13.
# python3-grpcio and python3-protobuf are the distribution's: the app builds its
# message classes at run time from a descriptor, so it is not tied to the
# protobuf version that made a generated module (linux/README.md).
cat >"$STAGE/DEBIAN/control" <<CONTROL
Package: plaitway
Version: $VERSION
Architecture: $ARCH
Maintainer: $MAINTAINER
Installed-Size: $(du -sk "$STAGE/usr" | cut -f1)
Depends: $shlibs, openvpn (>= 2.6), systemd, python3 (>= 3.10), python3-gi, gir1.2-gtk-4.0 (>= 4.14), gir1.2-adw-1 (>= 1.7), gir1.2-secret-1 (>= 0.20), python3-grpcio (>= 1.51), python3-protobuf (>= 3.21)
Recommends: systemd-resolved, polkitd, gnome-keyring | kwallet6
Suggests: gnome-shell-extension-appindicator | gnome-shell-ubuntu-extensions
Section: net
Priority: optional
Homepage: https://github.com/KoukeNeko/Plaitway
Description: several OpenVPN and WireGuard profiles at once
 Plaitway runs OpenVPN and WireGuard profiles side by side. One helper, the
 systemd service plaitwayd, runs as root and owns the routes and the DNS
 settings, so profiles do not fight over them and a network change leaves
 nothing stale behind.
 .
 The package has the helper, the command line client plaitway and the GTK 4
 app plaitway-app. OpenVPN is not bundled: it is the openvpn package. DNS
 settings are applied through systemd-resolved.
CONTROL
for script in postinst prerm postrm; do
    install -m 0755 "$PACKAGING_LINUX/debian/$script" "$STAGE/DEBIAN/$script"
done

log "writing md5sums"
(cd "$STAGE" && find . -path ./DEBIAN -prune -o -type f -exec md5sum {} + | sed 's|  \./|  |' | LC_ALL=C sort -k2) >"$STAGE/DEBIAN/md5sums"
chmod 0644 "$STAGE/DEBIAN/md5sums" "$STAGE/DEBIAN/control"

# dpkg-deb clamps the dates to SOURCE_DATE_EPOCH. -Zxz: every dpkg since 1.15
# reads it.
DEB="$OUT/plaitway_${VERSION}_${ARCH}.deb"
rm -f "$DEB"
log "building $DEB"
chmod 0755 "$STAGE" "$STAGE/DEBIAN"
dpkg-deb --root-owner-group -Zxz --build "$STAGE" "$DEB"
rm -rf "$WORK"
log "built $DEB"
