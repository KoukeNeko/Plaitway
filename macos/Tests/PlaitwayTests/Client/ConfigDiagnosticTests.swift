import GRPCCore
import PlaitwayClient
import Testing

struct ConfigDiagnosticTests {
    // The messages are the daemon's own: internal/ovpn and internal/wg (ParseError),
    // internal/profile (the checks every text goes through) and the fake backend.
    @Test(arguments: [
        (#"line 6: route: "notamask" is not a netmask"#, 6, #"route: "notamask" is not a netmask"#),
        ("line 3: ca refers to a file; put the content inline in <ca> ... </ca>", 3, "ca refers to a file; put the content inline in <ca> ... </ca>"),
        ("line 4: auth-user-pass refers to a file; use it without an argument", 4, "auth-user-pass refers to a file; use it without an argument"),
        ("line 9: <key> is never closed", 9, "<key> is never closed"),
        ("line 12: text after </key>", 12, "text after </key>"),
        ("line 2: carriage return without line feed", 2, "carriage return without line feed"),
        ("line 5: line is longer than 4096 bytes", 5, "line is longer than 4096 bytes"),
        ("line 2: PrivateKey is not a base64-encoded 32-byte key", 2, "PrivateKey is not a base64-encoded 32-byte key"),
        ("line 8: PostUp is not allowed: profiles cannot run commands", 8, "PostUp is not allowed: profiles cannot run commands"),
        (#"line 3: unknown directive "foo""#, 3, #"unknown directive "foo""#),
        ("line 5: more than one [Interface] section", 5, "more than one [Interface] section"),
        ("line 1: [Interface] has no PrivateKey", 1, "[Interface] has no PrivateKey"),
        (##"line 9: rejected by "# fake: reject""##, 9, ##"rejected by "# fake: reject""##),
        ("line 120000: x", 120_000, "x"),
        ("line 2: first reason\nsecond line of it", 2, "first reason\nsecond line of it"),
    ] as [(String, Int, String)])
    func readsTheLineOfARejection(message: String, line: Int, reason: String) {
        #expect(ConfigDiagnostic(rejection: message) == ConfigDiagnostic(line: line, message: reason))
    }

    @Test(arguments: [
        "profile has no remote server",
        "no [Interface] section",
        "no [Peer] section",
        "profile is empty",
        "profile is not valid UTF-8 text",
        "profile is larger than 1048576 bytes",
        "profile is 1048577 bytes, the limit is 1048576",
        "this profile is OpenVPN, but the text is WireGuard",
        "this profile is WireGuard, but the text is OpenVPN",
        "line is longer than it should be",
        "line 0: not a line",
        "line x: not a number",
        "line 3 is the problem",
        "line 99999999999999999999: too large for a line number",
        "Line 3: capital",
        "an error on line 3: in the middle",
        "",
    ])
    func aRejectionWithoutALineIsNotTiedToOne(message: String) {
        #expect(ConfigDiagnostic(rejection: message) == ConfigDiagnostic(line: nil, message: message))
    }

    @Test func readsTheRejectionOfAnRPCError() {
        let error = RPCError(code: .invalidArgument, message: "line 7: remote: bad")
        #expect(ConfigDiagnostic(error) == ConfigDiagnostic(line: 7, message: "remote: bad"))
        #expect(ConfigDiagnostic(DaemonFailure.rejected(message: "line 7: remote: bad")) == ConfigDiagnostic(line: 7, message: "remote: bad"))
    }

    @Test(arguments: [
        RPCError(code: .permissionDenied, message: "uid 501 is not an administrator, which /plaitway.v1.DaemonService/UpdateProfileContent requires"),
        RPCError(code: .unavailable, message: "the daemon is not running"),
        RPCError(code: .notFound, message: "line 3: not found"),
        RPCError(code: .internalError, message: "line 3: internal"),
    ])
    func anErrorThatIsNotARejectionOfTheTextIsNoDiagnostic(error: RPCError) {
        #expect(ConfigDiagnostic(error) == nil)
    }

    @Test func aForeignErrorIsNoDiagnostic() {
        struct Boom: Error {}
        #expect(ConfigDiagnostic(Boom()) == nil)
        #expect(ConfigDiagnostic(DaemonFailure.permissionDenied) == nil)
    }
}
