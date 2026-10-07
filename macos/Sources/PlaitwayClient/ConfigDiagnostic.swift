/// A problem the daemon found in profile text, for the editor to mark. The
/// daemon rejects a text with one reason, `line 12: route: "x" is not a
/// netmask`, or without a line when the problem is the text as a whole (`profile
/// has no remote server`) or its kind (`this profile is OpenVPN, but the text
/// is WireGuard`).
public struct ConfigDiagnostic: Equatable, Sendable {
    /// 1-based, counted in the text that was sent (with `SecretMask`, the restored
    /// text: see `SecretMask.Restoration.displayLine(forRestoredLine:)`). Nil when
    /// the problem is not tied to a line.
    public let line: Int?
    /// The daemon's reason, English and without its `line N: ` prefix.
    public let message: String

    public init(line: Int?, message: String) {
        self.line = line
        self.message = message
    }

    /// Reads the message of a rejection.
    public init(rejection: String) {
        if let match = rejection.wholeMatch(of: #/line ([0-9]+): (.*)/#.dotMatchesNewlines()),
           let line = Int(match.1), line > 0 {
            self.init(line: line, message: String(match.2))
        } else {
            self.init(line: nil, message: rejection)
        }
    }

    /// The diagnostic of an error from `ProfileStore.updateProfileContent` or
    /// `importProfile`. Nil when the daemon did not refuse the text: it is down,
    /// the caller is not an administrator, the profile is gone.
    public init?(_ error: any Error) {
        guard case .rejected(let message) = DaemonFailure(error) else { return nil }
        self.init(rejection: message)
    }
}
