#!/bin/sh
# Builds a signed apt repository from the .deb files of the releases:
#
#   packaging/linux/make-apt-repo.sh --debs DIR --out DIR --key KEY [--suite NAME] [--component NAME]
#
# DIR holds plaitway_<version>_<arch>.deb files, in any depth (every version
# that stays installable belongs there). OUT must not exist or be empty; it is
# the root of the repository:
#
#   pool/<component>/p/plaitway/*.deb
#   dists/<suite>/<component>/binary-<arch>/Packages and Packages.gz
#   dists/<suite>/Release, InRelease (signed in place) and Release.gpg (detached)
#   key.asc                                the public key, for signed-by
#
# KEY is a fingerprint or id of a secret key in the GnuPG home (GNUPGHOME or
# ~/.gnupg). If the key has a passphrase, set APT_GPG_PASSPHRASE. The suite is
# stable and the component main unless told otherwise; the architectures are the
# ones the .deb files are for. Needs apt-ftparchive (apt-utils), gpg and gzip.
set -eu
HERE="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib.sh
. "$HERE/lib.sh"

DEBS=""
OUT=""
KEY=""
SUITE=stable
COMPONENT=main
usage() { die "usage: $0 --debs DIR --out DIR --key KEY [--suite NAME] [--component NAME]"; }
while [ $# -gt 0 ]; do
    case "$1" in
        --debs) need_arg "$1" $#; DEBS="$2"; shift 2 ;;
        --out) need_arg "$1" $#; OUT="$2"; shift 2 ;;
        --key) need_arg "$1" $#; KEY="$2"; shift 2 ;;
        --suite) need_arg "$1" $#; SUITE="$2"; shift 2 ;;
        --component) need_arg "$1" $#; COMPONENT="$2"; shift 2 ;;
        *) usage ;;
    esac
done
[ -n "$DEBS" ] && [ -n "$OUT" ] && [ -n "$KEY" ] || usage
[ -d "$DEBS" ] || die "$DEBS is not a directory"
for tool in apt-ftparchive gpg gzip dpkg-deb; do
    command -v "$tool" >/dev/null 2>&1 || die "$tool is needed"
done
# The names end up in paths and in the Release file.
case "$SUITE$COMPONENT" in
    *[!A-Za-z0-9._-]*) die "the suite and the component are letters, digits, dots, dashes and underscores" ;;
esac
if [ -e "$OUT" ]; then
    [ -d "$OUT" ] && [ -z "$(ls -A "$OUT")" ] || die "$OUT exists and is not an empty directory"
fi

# gpg_run ARGS...: gpg without a terminal, with the passphrase of the key if
# there is one.
gpg_run() {
    if [ -n "${APT_GPG_PASSPHRASE:-}" ]; then
        printf '%s' "$APT_GPG_PASSPHRASE" | gpg --batch --yes --pinentry-mode loopback --passphrase-fd 0 --local-user "$KEY" "$@"
    else
        gpg --batch --yes --pinentry-mode loopback --local-user "$KEY" "$@"
    fi
}

mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
POOL="$OUT/pool/$COMPONENT/p/plaitway"
mkdir -p "$POOL"

# Only a package of ours, named the way the build names it, goes in: a stray
# file in the directory is not published under the signature of the project.
ARCHES=""
COUNT=0
LIST="$(mktemp)"
trap 'rm -f "$LIST"' EXIT
find "$DEBS" -type f -name '*.deb' | sort >"$LIST"
while IFS= read -r deb; do
    name="$(dpkg-deb -f "$deb" Package)"
    version="$(dpkg-deb -f "$deb" Version)"
    arch="$(dpkg-deb -f "$deb" Architecture)"
    [ "$name" = plaitway ] || die "$deb is the package $name, not plaitway"
    case "$arch" in
        amd64 | arm64) ;;
        *) die "$deb is for $arch; the repository has amd64 and arm64" ;;
    esac
    [ "$(basename "$deb")" = "plaitway_${version}_${arch}.deb" ] || die "$deb is not named plaitway_${version}_${arch}.deb"
    [ ! -e "$POOL/$(basename "$deb")" ] || die "plaitway_${version}_${arch}.deb is there twice"
    cp "$deb" "$POOL/"
    case " $ARCHES " in
        *" $arch "*) ;;
        *) ARCHES="${ARCHES:+$ARCHES }$arch" ;;
    esac
    COUNT=$((COUNT + 1))
done <"$LIST"
[ "$COUNT" -gt 0 ] || die "there is no .deb file in $DEBS"
ARCHES="$(printf '%s\n' $ARCHES | sort | tr '\n' ' ' | sed 's/ $//')"
log "$COUNT packages for $ARCHES"

# The paths in the index are relative to the root of the repository, which is
# where apt-ftparchive runs.
cd "$OUT"
for arch in $ARCHES; do
    index="dists/$SUITE/$COMPONENT/binary-$arch"
    mkdir -p "$index"
    apt-ftparchive --arch "$arch" packages "pool/$COMPONENT" >"$index/Packages"
    gzip -9 -n -c "$index/Packages" >"$index/Packages.gz"
done

log "writing and signing the Release file"
apt-ftparchive \
    -o "APT::FTPArchive::Release::Origin=Plaitway" \
    -o "APT::FTPArchive::Release::Label=Plaitway" \
    -o "APT::FTPArchive::Release::Suite=$SUITE" \
    -o "APT::FTPArchive::Release::Codename=$SUITE" \
    -o "APT::FTPArchive::Release::Architectures=$ARCHES" \
    -o "APT::FTPArchive::Release::Components=$COMPONENT" \
    release "dists/$SUITE" >"dists/$SUITE/Release"
gpg_run --clearsign --output "dists/$SUITE/InRelease" "dists/$SUITE/Release"
gpg_run --armor --detach-sign --output "dists/$SUITE/Release.gpg" "dists/$SUITE/Release"
gpg --batch --armor --export "$KEY" >key.asc
[ -s key.asc ] || die "the public key of $KEY could not be exported"

# What a client does with the files, once more, with only the exported key.
CHECK="$(mktemp -d)"
trap 'rm -f "$LIST"; rm -rf "$CHECK"' EXIT
chmod 700 "$CHECK"
GNUPGHOME="$CHECK" gpg --batch --import key.asc 2>/dev/null
GNUPGHOME="$CHECK" gpg --batch --verify "dists/$SUITE/InRelease" 2>/dev/null || die "InRelease does not verify with key.asc"
GNUPGHOME="$CHECK" gpg --batch --verify "dists/$SUITE/Release.gpg" "dists/$SUITE/Release" 2>/dev/null || die "Release.gpg does not verify with key.asc"
log "the repository is in $OUT"
