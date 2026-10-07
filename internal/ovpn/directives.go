package ovpn

import "strings"

// Policy for untrusted profiles. The daemon runs openvpn as root, so a profile
// decides three things for every directive:
//
//   - rejected: the directive makes openvpn read a file the profile names.
//     The whole profile is refused; certificates and keys must be inline.
//   - removed: the directive runs a program, loads code, writes files or
//     changes how the daemon runs openvpn. The profile is accepted without it
//     and a warning says so.
//   - kept: the directive is on the allow-list below. Anything that is on no
//     list is removed too, so a directive added to a future openvpn is not
//     trusted before it is reviewed here.

// rejectedFileDirectives read a file whose name the profile supplies. Each has
// an inline <tag> form that is accepted instead.
var rejectedFileDirectives = setOf(
	"ca", "capath", "cert", "key", "dh", "pkcs12", "tls-auth", "tls-crypt",
	"tls-crypt-v2", "crl-verify", "extra-certs", "secret", "client-config-dir",
	"pkcs11-providers",
)

// rejectedWithArgument are accepted bare and refused when they name a file:
// askpass and auth-user-pass read it, http-proxy-user-pass is bare only in its
// inline form.
var rejectedWithArgument = setOf("askpass", "auth-user-pass", "http-proxy-user-pass")

const (
	reasonProgram = "it runs an external program"
	reasonCode    = "it loads code into openvpn"
	reasonFiles   = "it makes openvpn read or write files"
	reasonDaemon  = "the daemon controls this"
	reasonUnknown = "it is not a supported directive"
	reasonOTP     = "it asks for a one-time code, which cannot be entered here"
)

// removedDirectives are dropped with a specific reason. Directives that match
// no list are dropped with reasonUnknown, so this table only exists to say
// why in a way the user can act on.
var removedDirectives = map[string]string{
	"up": reasonProgram, "down": reasonProgram, "up-delay": reasonProgram,
	"up-restart": reasonProgram, "down-pre": reasonProgram,
	"route-up": reasonProgram, "route-pre-down": reasonProgram,
	"ipchange": reasonProgram, "client-connect": reasonProgram,
	"client-disconnect": reasonProgram, "learn-address": reasonProgram,
	"tls-verify": reasonProgram, "auth-user-pass-verify": reasonProgram,
	"tls-crypt-v2-verify": reasonProgram, "iproute": reasonProgram,
	"dns-updown":      reasonProgram,
	"script-security": reasonProgram, "setenv": reasonProgram,
	"setenv-safe": reasonProgram, "echo": reasonProgram,
	"plugin": reasonCode, "engine": reasonCode, "providers": reasonCode,
	"config": reasonFiles, "log": reasonFiles, "log-append": reasonFiles,
	"syslog": reasonFiles, "status": reasonFiles, "status-version": reasonFiles,
	"writepid": reasonFiles, "dev-node": reasonFiles, "cd": reasonFiles,
	"chroot": reasonFiles, "tmp-dir": reasonFiles, "tls-export-cert": reasonFiles,
	"ifconfig-pool-persist": reasonFiles,
	"daemon":                reasonDaemon, "user": reasonDaemon, "group": reasonDaemon,
	"remap-usr1": reasonDaemon, "ifconfig-noexec": reasonDaemon,
	"inetd": reasonDaemon, "route-noexec": reasonDaemon,
	"block-outside-dns": "it is Windows only",
	"static-challenge":  reasonOTP,
}

// removedPrefixes cover whole families. management* would let the profile
// open its own control channel; pkcs11-* names smartcard modules; show-* and
// the like make openvpn print something and exit.
var removedPrefixes = map[string]string{
	"management": reasonDaemon,
	"pkcs11-":    reasonCode,
	"show-":      reasonDaemon,
}

// allowedDirectives are kept as written. They configure the connection, the
// crypto and the pulled options; none of them runs a program or touches a
// file by name. Options whose arguments need checking are in checkDirective.
var allowedDirectives = setOf(
	// connection
	"client", "tls-client", "pull", "remote", "remote-random", "remote-random-hostname",
	"resolv-retry", "connect-retry", "connect-retry-max", "connect-timeout",
	"server-poll-timeout", "nobind", "bind", "local", "lport", "rport", "port",
	"proto", "proto-force", "dev", "dev-type", "float", "fragment", "mssfix",
	"tun-mtu", "link-mtu", "tun-mtu-extra", "mtu-disc", "mtu-test", "sndbuf",
	"rcvbuf", "socket-flags", "tcp-queue-limit", "txqueuelen", "fast-io",
	"replay-window", "mute-replay-warnings", "explicit-exit-notify", "keepalive",
	"ping", "ping-exit", "ping-restart", "ping-timer-rem", "inactive",
	"persist-key", "persist-tun", "persist-local-ip", "persist-remote-ip",
	"disable-occ", "disable-dco",
	// addresses, routes and pushed options
	"topology", "ifconfig", "ifconfig-ipv6", "route", "route-ipv6", "route-gateway",
	"route-delay", "route-metric", "route-method", "route-nopull", "max-routes",
	"redirect-gateway", "redirect-private", "allow-pull-fqdn", "dhcp-option", "dns",
	"pull-filter", "push-peer-info", "ignore-unknown-option",
	// proxies
	"http-proxy", "http-proxy-option", "http-proxy-retry", "http-proxy-timeout",
	"http-proxy-user-pass", "socks-proxy", "socks-proxy-retry",
	// authentication
	"auth-user-pass", "auth-retry", "auth-nocache", "askpass",
	// crypto and TLS
	"auth", "cipher", "data-ciphers", "data-ciphers-fallback", "ncp-ciphers",
	"ncp-disable", "keysize", "tls-cipher", "tls-ciphersuites", "tls-cert-profile",
	"tls-version-min", "tls-version-max", "tls-groups", "ecdh-curve", "tls-timeout",
	"reneg-sec", "reneg-bytes", "reneg-pkts", "hand-window", "tran-window",
	"remote-cert-tls", "remote-cert-ku", "remote-cert-eku", "verify-x509-name",
	"verify-hash", "peer-fingerprint", "ns-cert-type", "x509-username-field",
	"key-direction", "key-method", "tls-exit", "single-session", "opt-verify",
	"use-prediction-resistance",
	// compression
	"compress", "comp-lzo", "allow-compression", "comp-noadapt",
	// logging detail only; the destination is the daemon's
	"verb", "mute", "suppress-timestamps",
)

// allowedBlocks are the inline forms of the rejected file directives and of
// the credential directives. connection is handled separately.
var allowedBlocks = setOf(
	"ca", "cert", "key", "dh", "pkcs12", "tls-auth", "tls-crypt", "tls-crypt-v2",
	"crl-verify", "extra-certs", "secret", "peer-fingerprint", "auth-user-pass",
	"http-proxy-user-pass",
)

func setOf(names ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(names))
	for _, n := range names {
		m[n] = struct{}{}
	}
	return m
}

func has(set map[string]struct{}, name string) bool {
	_, ok := set[name]
	return ok
}

// directiveName gives the name OpenVPN would look up: it drops one leading
// "--", which config files allow, and lower-cases so that the tables cannot be
// dodged by case. OpenVPN itself is case sensitive, so a differently cased
// name that slips through as "unknown" would only make openvpn refuse to start.
func directiveName(written string) string {
	return strings.ToLower(strings.TrimPrefix(written, "--"))
}

// removalReason says why a directive is dropped, or "" when it is not on the
// removal lists.
func removalReason(name string) string {
	if r, ok := removedDirectives[name]; ok {
		return r
	}
	for prefix, r := range removedPrefixes {
		if strings.HasPrefix(name, prefix) {
			return r
		}
	}
	return ""
}
