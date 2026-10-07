import Foundation
import Security
import Synchronization

/// What a profile asked for. `username` is empty for a key passphrase.
public struct Credentials: Codable, Equatable, Sendable {
    public var username: String
    public var password: String

    public init(username: String = "", password: String) {
        self.username = username
        self.password = password
    }
}

/// Where the user's answers to credential requests are kept between
/// connections. The daemon never keeps them on disk, so this is the only copy.
/// The calls are async because the Keychain may stop to ask the user for
/// permission, which must not freeze the app.
public protocol CredentialStore: Sendable {
    func credentials(profileID: String, kind: CredentialKind) async throws -> Credentials?
    /// Whether there is an answer, without reading it; that needs no permission.
    func hasCredentials(profileID: String, kind: CredentialKind) async throws -> Bool
    func save(_ credentials: Credentials, profileID: String, kind: CredentialKind) async throws
    func remove(profileID: String, kind: CredentialKind) async throws
}

/// The user's login Keychain: one generic password per profile and kind, with
/// the user name and the password in the item's data.
public struct KeychainCredentialStore: CredentialStore {
    public struct Failure: Error, CustomStringConvertible {
        public let status: OSStatus
        public let operation: String

        public var description: String {
            let message = SecCopyErrorMessageString(status, nil) as String? ?? "status \(status)"
            return "Keychain \(operation) failed: \(message)"
        }
    }

    public static let defaultService = "io.github.koukeneko.plaitway.credentials"

    private let service: String

    public init(service: String = defaultService) {
        self.service = service
    }

    @concurrent
    public func credentials(profileID: String, kind: CredentialKind) async throws -> Credentials? {
        var query = identity(profileID: profileID, kind: kind)
        query[kSecReturnData] = true
        query[kSecMatchLimit] = kSecMatchLimitOne
        var result: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &result)
        switch status {
        case errSecSuccess:
            guard let data = result as? Data else { return nil }
            return try JSONDecoder().decode(Credentials.self, from: data)
        case errSecItemNotFound:
            return nil
        default:
            throw Failure(status: status, operation: "read")
        }
    }

    @concurrent
    public func hasCredentials(profileID: String, kind: CredentialKind) async throws -> Bool {
        var query = identity(profileID: profileID, kind: kind)
        query[kSecReturnAttributes] = true
        query[kSecMatchLimit] = kSecMatchLimitOne
        switch SecItemCopyMatching(query as CFDictionary, nil) {
        case errSecSuccess: return true
        case errSecItemNotFound: return false
        case let status: throw Failure(status: status, operation: "read")
        }
    }

    @concurrent
    public func save(_ credentials: Credentials, profileID: String, kind: CredentialKind) async throws {
        let data = try JSONEncoder().encode(credentials)
        var item = identity(profileID: profileID, kind: kind)
        item[kSecValueData] = data
        var status = SecItemAdd(item as CFDictionary, nil)
        if status == errSecDuplicateItem {
            status = SecItemUpdate(
                identity(profileID: profileID, kind: kind) as CFDictionary,
                [kSecValueData: data] as CFDictionary
            )
        }
        guard status == errSecSuccess else { throw Failure(status: status, operation: "write") }
    }

    @concurrent
    public func remove(profileID: String, kind: CredentialKind) async throws {
        let status = SecItemDelete(identity(profileID: profileID, kind: kind) as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else {
            throw Failure(status: status, operation: "delete")
        }
    }

    private func identity(profileID: String, kind: CredentialKind) -> [CFString: Any] {
        [
            kSecClass: kSecClassGenericPassword,
            kSecAttrService: service,
            kSecAttrAccount: "\(profileID):\(kind.keychainName)",
        ]
    }
}

private extension CredentialKind {
    var keychainName: String {
        switch self {
        case .keyPassphrase: "key-passphrase"
        case .userPassword, .unspecified, .UNRECOGNIZED: "user-password"
        }
    }
}

/// For tests and for running without a Keychain.
public final class InMemoryCredentialStore: CredentialStore {
    private struct Key: Hashable {
        let profileID: String
        let kind: Int
    }

    private let items = Mutex<[Key: Credentials]>([:])

    public init() {}

    public func credentials(profileID: String, kind: CredentialKind) async throws -> Credentials? {
        items.withLock { $0[Key(profileID: profileID, kind: kind.rawValue)] }
    }

    public func hasCredentials(profileID: String, kind: CredentialKind) async throws -> Bool {
        items.withLock { $0[Key(profileID: profileID, kind: kind.rawValue)] != nil }
    }

    public func save(_ credentials: Credentials, profileID: String, kind: CredentialKind) async throws {
        items.withLock { $0[Key(profileID: profileID, kind: kind.rawValue)] = credentials }
    }

    public func remove(profileID: String, kind: CredentialKind) async throws {
        items.withLock { _ = $0.removeValue(forKey: Key(profileID: profileID, kind: kind.rawValue)) }
    }
}
