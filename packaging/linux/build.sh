#!/bin/sh
# Builds what install.sh lays out: plaitwayd and plaitway, the third-party
# notices and the changelog, in build/linux (or the directory of
# PLAITWAY_LINUX_BUILD, or --out).
#
#   packaging/linux/build.sh [--out DIR]
#
# The Go programs are built for the machine's own architecture, with cgo: the
# daemon asks the account database which users are administrators, and os/user
# follows the name service switch (sssd, LDAP, systemd-homed) only when it is
# built with cgo; without it only /etc/passwd and /etc/group are read, and an
# administrator who is not listed there would be refused. The programs then
# link the C library dynamically, and the package depends on it (see
# build-deb.sh). Set GOARCH and CC for a cross build, which also needs a C
# compiler and C library for the target; the CI builds each architecture on a
# machine of its own.
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
read_version

mkdir -p "$OUT/bin"
cd "$ROOT"

log "building plaitwayd and plaitway $VERSION"
export CGO_ENABLED=1
for program in plaitwayd plaitway; do
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$OUT/bin/$program" "./cmd/$program"
done

log "generating the third-party notices"
go run ./packaging/notices -platform linux -o "$OUT/THIRD_PARTY_NOTICES.md"

# The changelog is in the format of Debian, which dpkg-parsechangelog reads: one
# entry, for this version, from the notes of the release. Older releases were
# never packaged for Linux. The date is that of the last commit, so that
# building twice gives the same file; SOURCE_DATE_EPOCH overrides it.
log "writing the changelog"
epoch="${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct 2>/dev/null || date +%s)}"
write_changelog "$VERSION" "$ROOT/releases/$VERSION.md" "$MAINTAINER" "$epoch" >"$OUT/changelog"

log "built in $OUT"
