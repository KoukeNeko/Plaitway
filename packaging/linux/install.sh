#!/bin/sh
# The install map of the Linux version: the one place that says which file goes
# where. The Debian package (build-deb.sh) and `make install` both run it, and
# verify-deb.sh compares the package with what it installs.
#
#   packaging/linux/install.sh [--prefix PREFIX] [--destdir DIR] [--build DIR]
#                              [--python-dir DIR] [--version X.Y.Z]
#
# It lays out, below DESTDIR and PREFIX (default /usr/local):
#
#   libexec/plaitway/plaitwayd                the daemon, which is not on PATH
#   bin/plaitway                              the command line client
#   bin/plaitway-app                          the GTK app's launcher
#   lib/systemd/system/plaitwayd.service      the unit
#   lib/python3/dist-packages/plaitway/       the app's Python package
#   share/                                    linux/data/share: desktop entry,
#                                             metainfo, MIME types, icons
#   share/doc/plaitway/                       copyright, THIRD_PARTY_NOTICES.md,
#                                             changelog.gz
#
# The programs, the notices and the changelog come from the build directory
# (build.sh; default build/linux). It starts nothing and refreshes no cache:
# the package's maintainer scripts do that, and `make install` says what to run.
set -eu
HERE="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib.sh
. "$HERE/lib.sh"

PREFIX=/usr/local
DESTDIR=""
PYTHON_DIR=""
VERSION=""
while [ $# -gt 0 ]; do
    case "$1" in
        --prefix) need_arg "$1" $#; PREFIX="$2"; shift 2 ;;
        --destdir) need_arg "$1" $#; DESTDIR="$2"; shift 2 ;;
        --build) need_arg "$1" $#; BUILD_DIR="$2"; shift 2 ;;
        --python-dir) need_arg "$1" $#; PYTHON_DIR="$2"; shift 2 ;;
        --version) need_arg "$1" $#; VERSION="$2"; shift 2 ;;
        *) die "usage: $0 [--prefix PREFIX] [--destdir DIR] [--build DIR] [--python-dir DIR] [--version X.Y.Z]" ;;
    esac
done
case "$PREFIX" in /*) ;; *) die "--prefix must be an absolute path, got '$PREFIX'" ;; esac
PREFIX="${PREFIX%/}"
[ -n "$VERSION" ] || read_version

# The app's package goes where the system's Python looks. /usr/lib/python3/dist-packages
# is where Debian puts the packages it installs; for another prefix the Python on
# the machine says which of its directories lies below it.
if [ -z "$PYTHON_DIR" ]; then
    if [ "$PREFIX" = /usr ]; then
        PYTHON_DIR=/usr/lib/python3/dist-packages
    else
        PYTHON_DIR="$(python3 -c 'import sys; print("\n".join(sys.path))' 2>/dev/null |
            grep -E "^$PREFIX/lib/python3[^/]*/(dist|site)-packages\$" | head -n 1 || true)"
        [ -n "$PYTHON_DIR" ] || die "no directory of python3's sys.path lies below $PREFIX; pass --python-dir"
    fi
fi

for input in bin/plaitwayd bin/plaitway THIRD_PARTY_NOTICES.md changelog; do
    [ -f "$BUILD_DIR/$input" ] || die "$BUILD_DIR/$input is missing; run packaging/linux/build.sh"
done
# The lists below are made in pipelines, whose failure would not stop the script.
for input in linux/src/plaitway linux/data/share; do
    [ -d "$ROOT/$input" ] || die "$ROOT/$input is missing"
done

umask 022
root="$DESTDIR$PREFIX"
python_dest="$DESTDIR$PYTHON_DIR/plaitway"
doc="$root/share/doc/plaitway"

# make_dir PATH: PATH and the parents below DESTDIR, each mode 0755 whatever the umask.
make_dir() {
    case "$1" in "$DESTDIR"/*) ;; *) die "internal error: $1 is outside $DESTDIR" ;; esac
    md_dir="$1"
    md_missing=""
    while [ ! -d "$md_dir" ] && [ "$md_dir" != "$DESTDIR" ] && [ "$md_dir" != / ]; do
        md_missing="$md_dir $md_missing"
        md_dir="$(dirname "$md_dir")"
    done
    for md_dir in $md_missing; do install -d -m 0755 "$md_dir"; done
}

# put MODE SOURCE DESTINATION installs one file.
put() {
    make_dir "$(dirname "$3")"
    install -m "$1" "$2" "$3"
}

log "installing into $root"
put 0755 "$BUILD_DIR/bin/plaitwayd" "$root/libexec/$LIBEXEC_NAME/plaitwayd"
put 0755 "$BUILD_DIR/bin/plaitway" "$root/bin/plaitway"
put 0755 "$ROOT/linux/bin/plaitway-app" "$root/bin/plaitway-app"

# The unit names the daemon by its package path. For another prefix the path is
# rewritten, and nothing else.
unit_dest="$root/lib/systemd/system/$UNIT_NAME"
make_dir "$(dirname "$unit_dest")"
sed "s|^ExecStart=/usr/libexec/|ExecStart=$PREFIX/libexec/|" "$PACKAGING_LINUX/$UNIT_NAME" >"$unit_dest"
chmod 0644 "$unit_dest"
grep -q "^ExecStart=$PREFIX/libexec/$LIBEXEC_NAME/plaitwayd " "$unit_dest" || die "$UNIT_NAME has no ExecStart for $PREFIX/libexec/$LIBEXEC_NAME/plaitwayd"

# The Python package, without caches. The release version replaces the
# development one in the installed copy only; the app compares it with the
# daemon's.
(cd "$ROOT/linux/src" && find plaitway -name __pycache__ -prune -o -type d -print) |
    while IFS= read -r dir; do make_dir "$DESTDIR$PYTHON_DIR/$dir"; done
(cd "$ROOT/linux/src" && find plaitway -name __pycache__ -prune -o -type f ! -name '*.pyc' -print) |
    while IFS= read -r file; do put 0644 "$ROOT/linux/src/$file" "$DESTDIR$PYTHON_DIR/$file"; done
version_file="$python_dest/version.py"
grep -q '^__version__ = "0.0.0-dev"$' "$version_file" || die "$version_file does not hold the development version to replace"
sed "s|^__version__ = \"0.0.0-dev\"\$|__version__ = \"$VERSION\"|" "$version_file" >"$version_file.new"
mv "$version_file.new" "$version_file"
chmod 0644 "$version_file"

# linux/data/share, as it is.
(cd "$ROOT/linux/data/share" && find . -type f -print) |
    while IFS= read -r file; do put 0644 "$ROOT/linux/data/share/$file" "$root/share/${file#./}"; done

put 0644 "$PACKAGING_LINUX/copyright" "$doc/copyright"
put 0644 "$BUILD_DIR/THIRD_PARTY_NOTICES.md" "$doc/THIRD_PARTY_NOTICES.md"
make_dir "$doc"
# -n: no name and no time in the file, so that the same input gives the same bytes.
gzip -9 -n -c "$BUILD_DIR/changelog" >"$doc/changelog.gz"
chmod 0644 "$doc/changelog.gz"

log "installed $VERSION"
