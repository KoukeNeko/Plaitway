import GRPCCore

/// Why a call to the daemon failed, in the terms a UI words differently
/// (`RPCError` carries codes and daemon-side text). `message` is the daemon's
/// English text and is meant to be shown only where the cause is the input,
/// such as a rejected profile.
public enum DaemonFailure: Error, Equatable, Sendable {
    /// The caller is not an administrator, or not the console user.
    case permissionDenied
    /// The daemon is not running or not reachable.
    case unavailable
    case notFound
    /// The daemon refused the input; `message` is its reason.
    case rejected(message: String)
    case other(message: String)

    public init(_ error: any Error) {
        if let failure = error as? DaemonFailure {
            self = failure
            return
        }
        guard let rpc = error as? RPCError else {
            self = .other(message: error.localizedDescription)
            return
        }
        switch rpc.code {
        case .permissionDenied: self = .permissionDenied
        case .unavailable: self = .unavailable
        case .notFound: self = .notFound
        case .invalidArgument, .failedPrecondition: self = .rejected(message: rpc.message)
        default: self = .other(message: rpc.message)
        }
    }
}
