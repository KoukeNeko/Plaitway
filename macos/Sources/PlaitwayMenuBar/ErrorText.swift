import PlaitwayClient

/// What the user is told about a failure. The daemon's own text is shown only
/// where it explains the input (a rejected profile).
func userMessage(for error: any Error) -> String {
    if let failure = error as? ProfileImporter.Failure {
        return failure.message
    }
    if error is DaemonInstaller.Refusal {
        return String(localized: "Move Plaitway to the Applications folder and open it again.", bundle: .module)
    }
    switch DaemonFailure(error) {
    case .permissionDenied: return String(localized: "Administrator required", bundle: .module)
    case .unavailable: return String(localized: "Helper unavailable", bundle: .module)
    case .notFound: return String(localized: "Profile not found", bundle: .module)
    case .rejected(let message), .other(let message): return message
    }
}

extension ProfileImporter.Failure {
    var message: String {
        switch self {
        case .unreadable(let path, let reason):
            String(localized: "Cannot read \(path): \(reason)", bundle: .module)
        case .notText(let path):
            String(localized: "Not a text file: \(path)", bundle: .module)
        case .tooLarge(let path):
            String(localized: "File too large: \(path)", bundle: .module)
        case .missingFile(let directive, let path):
            String(localized: "File for \(directive) not found: \(path)", bundle: .module)
        case .notKeyMaterial(let directive, let path):
            String(localized: "File for \(directive) holds no certificate or key: \(path)", bundle: .module)
        case .outsideProfileDirectory(let directive, let path):
            String(localized: "File for \(directive) is outside the profile's folder: \(path)", bundle: .module)
        case .notCredentials(let path):
            String(localized: "File for auth-user-pass holds no user name and password: \(path)", bundle: .module)
        }
    }
}
