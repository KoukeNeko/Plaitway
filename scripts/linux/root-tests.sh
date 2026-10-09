#!/bin/sh
# Builds the rootintegration tests of the Linux packages and runs each of them
# in a private user, network and mount namespace: the fake root of the namespace
# changes the routes, links and addresses of a network that has nothing in it
# but loopback, and the machine's own network is out of reach. Nothing needs
# sudo, and the tests refuse to run anywhere else.
#
#   scripts/linux/root-tests.sh [PACKAGE...]
#
# PACKAGE is a directory of the repository (default: internal/osnet/linux,
# internal/reconciler, internal/wg, internal/ovpn and cmd/plaitwayd). It needs
# ip, ping and wg (iproute2, iputils-ping, wireguard-tools), the openvpn of the
# distribution, and for the DNS test of internal/osnet/linux dbus-daemon and
# systemd-resolved; a test that lacks what it needs skips and says why. Ubuntu
# 24.04 and later restrict unprivileged user namespaces with AppArmor; where
# the namespace cannot be made, run `sudo sysctl -w
# kernel.apparmor_restrict_unprivileged_userns=0` first.
set -eu
HERE="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=../../packaging/linux/lib.sh
. "$HERE/../../packaging/linux/lib.sh"

PACKAGES="internal/osnet/linux internal/reconciler internal/wg internal/ovpn cmd/plaitwayd"
[ $# -eq 0 ] || PACKAGES="$*"

# The tests refuse the machine's own user namespace, which root has.
[ "$(id -u)" -ne 0 ] || die "run this as an unprivileged user: the tests need a user namespace of their own, and refuse root's"
for tool in go unshare ip; do
    command -v "$tool" >/dev/null 2>&1 || die "$tool is needed"
done
unshare -Urnm true 2>/dev/null || die "cannot make a user, network and mount namespace (on Ubuntu 24.04 and later: sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0)"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# run_in_namespace DIRECTORY TEST_BINARY [TEST_FLAGS...]: the binary as the root
# of a private namespace, with a tmpfs on /run (ip-netns keeps the peer's
# namespace there, and it hides the machine's D-Bus and systemd-resolved sockets)
# and loopback up.
run_in_namespace() {
    directory="$1"
    binary="$2"
    shift 2
    (
        cd "$directory"
        unshare -Urnm sh -c 'mount -t tmpfs none /run && ip link set lo up && PLAITWAY_ROOT_TESTS=1 exec "$@"' sh "$binary" -test.v "$@"
    )
}

failed=""
for package in $PACKAGES; do
    binary="$WORK/$(printf '%s' "$package" | tr / _).test"
    log "building the root tests of $package"
    (cd "$ROOT" && go test -c -tags rootintegration -o "$binary" "./$package")
    log "running $package"
    case "$package" in
        # The DNS test starts a systemd-resolved of its own in the namespace and
        # leaves a link behind, which the route tests then refuse: it runs alone.
        internal/osnet/linux)
            run_in_namespace "$ROOT/$package" "$binary" '-test.run=^TestRoot' || failed="$failed $package"
            run_in_namespace "$ROOT/$package" "$binary" '-test.run=^TestResolvedEndToEnd$' || failed="$failed $package(dns)"
            ;;
        internal/reconciler)
            run_in_namespace "$ROOT/$package" "$binary" '-test.run=^TestKernel' || failed="$failed $package"
            ;;
        # The other tests of a package run under go test, and some of them see
        # root's files as another user's inside a user namespace.
        *)
            run_in_namespace "$ROOT/$package" "$binary" '-test.run=^TestRoot' || failed="$failed $package"
            ;;
    esac
done

if [ -n "$failed" ]; then
    printf 'failed:%s\n' "$failed" >&2
    exit 1
fi
log "all root tests passed"
