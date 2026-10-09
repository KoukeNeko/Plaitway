#!/bin/sh
# Tests of make-apt-repo.sh with throwaway keys and stand-in packages, and of the
# repository it makes with the apt of this machine, as a person who adds the
# repository would use it. They need no root and no network.
# `make test-packaging-linux` runs it. A machine without apt-ftparchive
# (apt-utils), gpg or dpkg-deb skips them and says so.
set -u
# The checks read the words of apt and gpg.
LC_ALL=C
export LC_ALL
HERE="$(cd "$(dirname "$0")" && pwd)"

for tool in apt-ftparchive gpg dpkg-deb apt-get apt-cache; do
    command -v "$tool" >/dev/null 2>&1 || { printf 'skip  the apt repository tests need %s\n' "$tool"; exit 0; }
done

TMP="$(mktemp -d)"
GNUPGHOME="$TMP/gnupg"
export GNUPGHOME
mkdir -m 700 "$GNUPGHOME"
# The agent of the throwaway keys goes with them.
trap 'gpgconf --kill gpg-agent >/dev/null 2>&1; rm -rf "$TMP"' EXIT
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
# run COMMAND...: runs it, keeping its output and status.
run() {
    OUTPUT="$("$@" 2>&1)"
    STATUS=$?
}

# --- the keys and the packages --------------------------------------------------

gpg --batch --passphrase '' --quick-generate-key "Plaitway Test <test@example.invalid>" rsa2048 sign never >/dev/null 2>&1
KEY="$(gpg --list-secret-keys --with-colons 2>/dev/null | awk -F: '/^fpr/ {print $10; exit}')"
gpg --batch --passphrase 'a passphrase' --quick-generate-key "Protected <protected@example.invalid>" rsa2048 sign never >/dev/null 2>&1
PROTECTED="$(gpg --list-secret-keys --with-colons protected@example.invalid 2>/dev/null | awk -F: '/^fpr/ {print $10; exit}')"
gpg --batch --passphrase '' --quick-generate-key "Other <other@example.invalid>" rsa2048 sign never >/dev/null 2>&1
OTHER="$(gpg --list-secret-keys --with-colons other@example.invalid 2>/dev/null | awk -F: '/^fpr/ {print $10; exit}')"
[ -n "$KEY" ] && [ -n "$PROTECTED" ] && [ -n "$OTHER" ] || { echo "the test keys could not be made"; exit 1; }

# make_deb DIRECTORY PACKAGE VERSION ARCH: a package that installs nothing.
make_deb() {
    tree="$TMP/tree/$2_$3_$4"
    mkdir -p "$tree/DEBIAN" "$1"
    printf 'Package: %s\nVersion: %s\nArchitecture: %s\nMaintainer: Test <test@example.invalid>\nDescription: stand-in\n' "$2" "$3" "$4" >"$tree/DEBIAN/control"
    dpkg-deb --root-owner-group --build "$tree" "$1/$2_$3_$4.deb" >/dev/null
}
DEBS="$TMP/debs"
make_deb "$DEBS/v1.0.0" plaitway 1.0.0 amd64
make_deb "$DEBS/v1.0.0" plaitway 1.0.0 arm64
make_deb "$DEBS/v1.1.0" plaitway 1.1.0 amd64
make_deb "$DEBS/v1.1.0" plaitway 1.1.0 arm64

# --- the repository -----------------------------------------------------------------

REPO="$TMP/repo"
run "$HERE/make-apt-repo.sh" --debs "$DEBS" --out "$REPO" --key "$KEY"
expect "the repository is made from packages in directories of their own" '[ "$STATUS" -eq 0 ]'
expect "every package is in the pool, under the project's name" '[ "$(ls "$REPO/pool/main/p/plaitway" | wc -l)" -eq 4 ] && [ -f "$REPO/pool/main/p/plaitway/plaitway_1.1.0_arm64.deb" ]'
expect "there is an index for each architecture, plain and compressed" 'for a in amd64 arm64; do [ -s "$REPO/dists/stable/main/binary-$a/Packages" ] && [ -s "$REPO/dists/stable/main/binary-$a/Packages.gz" ] || exit 1; done'
expect "the amd64 index lists both versions and no other architecture" '[ "$(grep -c "^Package: plaitway$" "$REPO/dists/stable/main/binary-amd64/Packages")" -eq 2 ] && ! grep -q "^Architecture: arm64$" "$REPO/dists/stable/main/binary-amd64/Packages"'
expect "the paths in the index are relative to the root and exist" 'grep "^Filename: " "$REPO/dists/stable/main/binary-amd64/Packages" | sed "s/^Filename: //" | while read -r f; do [ -f "$REPO/$f" ] || exit 1; done'
expect "the Release file names the suite, the architectures and the component" 'grep -qx "Suite: stable" "$REPO/dists/stable/Release" && grep -qx "Codename: stable" "$REPO/dists/stable/Release" && grep -qx "Architectures: amd64 arm64" "$REPO/dists/stable/Release" && grep -qx "Components: main" "$REPO/dists/stable/Release"'
expect "the Release file has the hashes of the indexes" 'grep -q "main/binary-amd64/Packages.gz" "$REPO/dists/stable/Release" && grep -q "^SHA256:" "$REPO/dists/stable/Release"'
expect "InRelease and Release.gpg are there, with the public key" '[ -s "$REPO/dists/stable/InRelease" ] && [ -s "$REPO/dists/stable/Release.gpg" ] && grep -q "BEGIN PGP PUBLIC KEY BLOCK" "$REPO/key.asc"'
# who_signed: the fingerprint gpg reports for InRelease, with only the exported key.
who_signed() (
    GNUPGHOME="$TMP/verify"
    export GNUPGHOME
    mkdir -m 700 "$GNUPGHOME"
    gpg --batch --import "$REPO/key.asc" 2>/dev/null
    gpg --batch --verify "$REPO/dists/stable/InRelease" 2>&1
    gpgconf --kill gpg-agent >/dev/null 2>&1
)
OUTPUT="$(who_signed)"
expect "the exported key is the one that signed" 'output_has "$KEY"'

# A repository made again from the same packages is the same but for the date.
run "$HERE/make-apt-repo.sh" --debs "$DEBS" --out "$REPO" --key "$KEY"
expect "a directory that is not empty is not used" '[ "$STATUS" -ne 0 ] && output_has "not an empty directory"'

run "$HERE/make-apt-repo.sh" --debs "$DEBS" --out "$TMP/repo-protected" --key "$PROTECTED"
expect "a key with a passphrase cannot sign without it" '[ "$STATUS" -ne 0 ]'
run env APT_GPG_PASSPHRASE='a passphrase' "$HERE/make-apt-repo.sh" --debs "$DEBS" --out "$TMP/repo-protected2" --key "$PROTECTED"
expect "a key with a passphrase signs with APT_GPG_PASSPHRASE" '[ "$STATUS" -eq 0 ] && [ -s "$TMP/repo-protected2/dists/stable/InRelease" ]'
# The agent keeps a passphrase it was given, so the wrong one needs a new agent.
gpgconf --kill gpg-agent >/dev/null 2>&1
run env APT_GPG_PASSPHRASE='the wrong one' "$HERE/make-apt-repo.sh" --debs "$DEBS" --out "$TMP/repo-protected3" --key "$PROTECTED"
expect "a wrong passphrase is an error" '[ "$STATUS" -ne 0 ]'

run "$HERE/make-apt-repo.sh" --debs "$DEBS" --out "$TMP/repo-suite" --key "$KEY" --suite noble --component extra
expect "the suite and the component can be named" '[ "$STATUS" -eq 0 ] && [ -s "$TMP/repo-suite/dists/noble/extra/binary-amd64/Packages" ] && grep -qx "Suite: noble" "$TMP/repo-suite/dists/noble/Release"'
run "$HERE/make-apt-repo.sh" --debs "$DEBS" --out "$TMP/repo-badname" --key "$KEY" --suite 'a/b'
expect "a suite with a slash is refused" '[ "$STATUS" -ne 0 ] && [ ! -e "$TMP/repo-badname" ]'

# What may go into the repository.
mkdir -p "$TMP/empty"
run "$HERE/make-apt-repo.sh" --debs "$TMP/empty" --out "$TMP/repo-empty" --key "$KEY"
expect "no package is an error" '[ "$STATUS" -ne 0 ] && output_has "no .deb file"'
make_deb "$TMP/other-package" somethingelse 1.0.0 amd64
run "$HERE/make-apt-repo.sh" --debs "$TMP/other-package" --out "$TMP/repo-other" --key "$KEY"
expect "a package of another name is refused" '[ "$STATUS" -ne 0 ] && output_has "not plaitway"'
make_deb "$TMP/wrong-arch" plaitway 1.0.0 i386
run "$HERE/make-apt-repo.sh" --debs "$TMP/wrong-arch" --out "$TMP/repo-arch" --key "$KEY"
expect "an architecture the repository does not have is refused" '[ "$STATUS" -ne 0 ] && output_has "i386"'
make_deb "$TMP/misnamed" plaitway 1.0.0 amd64
mv "$TMP/misnamed/plaitway_1.0.0_amd64.deb" "$TMP/misnamed/anything.deb"
run "$HERE/make-apt-repo.sh" --debs "$TMP/misnamed" --out "$TMP/repo-misnamed" --key "$KEY"
expect "a file that is not named after its package is refused" '[ "$STATUS" -ne 0 ] && output_has "is not named"'
mkdir -p "$TMP/twice"
make_deb "$TMP/twice/a" plaitway 1.0.0 amd64
make_deb "$TMP/twice/b" plaitway 1.0.0 amd64
run "$HERE/make-apt-repo.sh" --debs "$TMP/twice" --out "$TMP/repo-twice" --key "$KEY"
expect "the same package twice is refused" '[ "$STATUS" -ne 0 ] && output_has "there twice"'
run "$HERE/make-apt-repo.sh" --debs "$DEBS" --out "$TMP/repo-nokey"
expect "a missing key is a usage error" '[ "$STATUS" -ne 0 ] && output_has "usage"'
run "$HERE/make-apt-repo.sh" --debs "$DEBS" --out "$TMP/repo-unknown" --key 0000000000000000000000000000000000000000
expect "a key that is not in the keyring is an error" '[ "$STATUS" -ne 0 ]'

# --- apt reads it ---------------------------------------------------------------------
# apt-get of this machine, with a state and a cache of its own, as a user who is
# not root: add the repository with its key, update, look at the versions, and
# download the newest one.

ARCH="$(dpkg --print-architecture)"
case "$ARCH" in
    amd64 | arm64) ;;
    *) printf 'skip  apt on %s: the repository has amd64 and arm64\n' "$ARCH"; exit "$([ "$FAILURES" -eq 0 ] && echo 0 || echo 1)" ;;
esac

# apt_in NAME: sets APT_CONFIG for a client that knows only NAME's sources.
apt_in() {
    dir="$TMP/apt-$1"
    mkdir -p "$dir/state/lists/partial" "$dir/cache/archives/partial" "$dir/etc/sources.list.d" "$dir/etc/preferences.d" "$dir/etc/apt.conf.d"
    : >"$dir/status"
    cat >"$dir/apt.conf" <<EOF
Dir::State "$dir/state";
Dir::State::status "$dir/status";
Dir::Cache "$dir/cache";
Dir::Etc "$dir/etc";
Dir::Etc::sourcelist "$dir/etc/sources.list";
Dir::Etc::sourceparts "$dir/etc/sources.list.d";
Dir::Etc::trusted "$dir/etc/trusted.gpg";
Dir::Etc::trustedparts "$dir/etc/trusted.gpg.d";
Debug::NoLocking "true";
APT::Architecture "$ARCH";
APT::Architectures "$ARCH";
APT::Sandbox::User "";
EOF
    APT_CONFIG="$dir/apt.conf"
    export APT_CONFIG
}

apt_in good
printf 'deb [signed-by=%s] file:%s stable main\n' "$REPO/key.asc" "$REPO" >"$TMP/apt-good/etc/sources.list"
run apt-get update
expect "apt updates from the repository with its key" '[ "$STATUS" -eq 0 ] && ! output_has "^E:" && ! output_has "^W:"'
run apt-cache policy plaitway
expect "apt sees both versions and prefers the newest" 'output_has "Candidate: 1.1.0" && output_has "1.0.0"'
mkdir -p "$TMP/download"
run sh -c "cd '$TMP/download' && apt-get download plaitway"
expect "apt downloads the newest package from the pool and checks it" '[ "$STATUS" -eq 0 ] && [ "$(dpkg-deb -f "$TMP/download/plaitway_1.1.0_'"$ARCH"'.deb" Version)" = 1.1.0 ]'
run sh -c "cd '$TMP/download' && apt-get download plaitway=1.0.0"
expect "apt downloads an older version by number" '[ "$STATUS" -eq 0 ] && [ -f "$TMP/download/plaitway_1.0.0_'"$ARCH"'.deb" ]'

apt_in wrongkey
gpg --batch --armor --export "$OTHER" >"$TMP/other-key.asc"
printf 'deb [signed-by=%s] file:%s stable main\n' "$TMP/other-key.asc" "$REPO" >"$TMP/apt-wrongkey/etc/sources.list"
run apt-get update
expect "apt refuses the repository when the key is another one" 'output_has "NO_PUBKEY\|not signed\|signatures were invalid\|^E:"'

# A change after the signing: the index no longer has the hash in the Release file.
cp -r "$REPO" "$TMP/repo-tampered"
printf '\n' >>"$TMP/repo-tampered/dists/stable/main/binary-$ARCH/Packages"
rm -f "$TMP/repo-tampered/dists/stable/main/binary-$ARCH/Packages.gz"
gzip -9 -n -c "$TMP/repo-tampered/dists/stable/main/binary-$ARCH/Packages" >"$TMP/repo-tampered/dists/stable/main/binary-$ARCH/Packages.gz"
apt_in tampered
printf 'deb [signed-by=%s] file:%s stable main\n' "$REPO/key.asc" "$TMP/repo-tampered" >"$TMP/apt-tampered/etc/sources.list"
run apt-get update
expect "apt refuses an index that was changed after the signing" 'output_has "Hash Sum mismatch\|^E:\|^W:"'

printf '\n'
if [ "$FAILURES" -ne 0 ]; then
    printf '%s checks failed\n' "$FAILURES"
    exit 1
fi
printf 'all checks passed\n'
