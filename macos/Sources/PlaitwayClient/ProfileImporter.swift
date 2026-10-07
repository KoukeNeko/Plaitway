import Foundation

/// Reads a profile file for `ProfileStore.importProfile`. The daemon accepts
/// content only, never a path, so an OpenVPN profile that refers to its
/// certificates and keys by file name gets those files read here, with the
/// user's own permissions, and put inline as `<ca>`, `<cert>`, `<key>` and the
/// like. A profile is untrusted, so it may only name files inside its own
/// directory; `auth-user-pass <file>` becomes a bare `auth-user-pass` and the
/// file's user name and password go to the credential store.
public enum ProfileImporter {
    public enum Failure: Error, Equatable {
        /// The profile or a file it refers to cannot be read.
        case unreadable(path: String, reason: String)
        /// A file is not text; profiles and the files they refer to are.
        case notText(path: String)
        case tooLarge(path: String)
        /// A directive names a file that does not exist.
        case missingFile(directive: String, path: String)
        /// A directive names a file that holds no PEM data.
        case notKeyMaterial(directive: String, path: String)
        /// A directive names a file that is not inside the profile's directory.
        case outsideProfileDirectory(directive: String, path: String)
        /// An `auth-user-pass` file without a user name and a password on its first two lines.
        case notCredentials(path: String)
    }

    /// A profile ready for `ProfileStore.importProfile`.
    public struct Loaded: Equatable, Sendable {
        /// The profile as the daemon takes it.
        public let content: Data
        /// What the profile's `auth-user-pass` file held, if it named one.
        public let credentials: Credentials?
    }

    /// The daemon refuses larger profiles (profile.MaxContentSize); a hopeless
    /// upload need not go over the socket.
    public static let maxProfileSize = 1 << 20
    /// Generous for a certificate chain or a key, small enough to rule out
    /// reading something that is not one.
    static let maxReferencedFileSize = 256 << 10

    /// Directives that name a file and have an inline `<tag>` form.
    private static let inlineDirectives: Set<String> = [
        "ca", "cert", "key", "dh", "crl-verify", "extra-certs", "tls-auth", "tls-crypt", "tls-crypt-v2",
    ]

    /// The profile as it is sent to the daemon, with the credentials that came with it.
    public static func load(from url: URL) throws -> Loaded {
        let data = try readText(at: url, maxSize: maxProfileSize)
        let text = String(decoding: data, as: UTF8.self)
        // A wg-quick file has no file references; its keys are inline already.
        guard isOpenVPN(text, filename: url.lastPathComponent) else { return Loaded(content: data, credentials: nil) }
        let inlined = try inlineReferencedFiles(in: text, relativeTo: url.deletingLastPathComponent())
        let result = Data(inlined.text.utf8)
        guard result.count <= maxProfileSize else { throw Failure.tooLarge(path: url.path) }
        return Loaded(content: result, credentials: inlined.credentials)
    }

    /// Replaces `ca file`, `tls-auth file 1` and the like by their inline
    /// blocks, and `auth-user-pass file` by `auth-user-pass`, returning the
    /// file's credentials. Paths are relative to the profile's directory, as for
    /// openvpn, and must stay inside it. Blocks that are already inline are left alone.
    public static func inlineReferencedFiles(
        in text: String, relativeTo directory: URL
    ) throws -> (text: String, credentials: Credentials?) {
        // Splitting on Character keeps CR LF together as one line break; the
        // result uses LF only.
        let lines = text.split(omittingEmptySubsequences: false, whereSeparator: \.isNewline).map(String.init)
        var hasKeyDirection = lines.contains { tokens(of: $0[...]).first?.lowercased() == "key-direction" }

        var output: [String] = []
        var credentials: Credentials?
        var openBlock: String?
        for line in lines {
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            if let tag = openBlock {
                if trimmed.lowercased() == "</\(tag)>" { openBlock = nil }
                output.append(line)
                continue
            }
            if let tag = blockTag(opening: trimmed) {
                openBlock = tag
                output.append(line)
                continue
            }
            if let path = credentialsFile(in: trimmed) {
                credentials = try readCredentials(path: path, directory: directory)
                output.append("auth-user-pass")
                continue
            }
            guard let reference = fileReference(in: trimmed) else {
                output.append(line)
                continue
            }

            let content = try readKeyMaterial(directive: reference.directive, path: reference.path, directory: directory)
            output.append("<\(reference.directive)>")
            output.append(content)
            output.append("</\(reference.directive)>")
            if let direction = reference.keyDirection, !hasKeyDirection {
                output.append("key-direction \(direction)")
                hasKeyDirection = true
            }
        }
        return (output.joined(separator: "\n"), credentials)
    }

    // MARK: Parsing

    private struct FileReference {
        let directive: String
        let path: String
        /// The optional 0 or 1 after a `tls-auth` file.
        let keyDirection: String?
    }

    private static func isOpenVPN(_ text: String, filename: String) -> Bool {
        if text.localizedCaseInsensitiveContains("[Interface]") { return false }
        return filename.lowercased().hasSuffix(".ovpn") || tokensOfLines(in: text).contains { $0.first == "remote" || $0.first == "client" }
    }

    private static func tokensOfLines(in text: String) -> [[String]] {
        text.split(whereSeparator: \.isNewline).map { tokens(of: $0).map { $0.lowercased() } }
    }

    private static func fileReference(in line: String) -> FileReference? {
        let words = tokens(of: Substring(line))
        guard words.count >= 2, inlineDirectives.contains(words[0].lowercased()) else { return nil }
        let directive = words[0].lowercased()
        // `dh none` switches Diffie-Hellman off; `[inline]` says the data follows.
        if (directive == "dh" && words[1] == "none") || words[1] == "[inline]" { return nil }
        let direction = directive == "tls-auth" && words.count >= 3 ? words[2] : nil
        return FileReference(directive: directive, path: words[1], keyDirection: direction)
    }

    /// The file of an `auth-user-pass file` line.
    private static func credentialsFile(in line: String) -> String? {
        let words = tokens(of: Substring(line))
        guard words.count >= 2, words[0].lowercased() == "auth-user-pass" else { return nil }
        return words[1]
    }

    /// `<ca>` opens a block; `</ca>` and `<connection>` style tags with
    /// arguments do not matter here, only that the lines in between are data.
    private static func blockTag(opening line: String) -> String? {
        guard line.hasPrefix("<"), line.hasSuffix(">"), !line.hasPrefix("</") else { return nil }
        let tag = line.dropFirst().dropLast().lowercased()
        return tag.isEmpty || tag.contains(" ") ? nil : String(tag)
    }

    /// Splits a configuration line into words like openvpn: double or single
    /// quotes group, a backslash escapes, and a line that starts with `#` or
    /// `;` is a comment.
    private static func tokens(of line: Substring) -> [String] {
        let trimmed = line.trimmingCharacters(in: .whitespaces)
        guard let first = trimmed.first, first != "#", first != ";" else { return [] }

        var words: [String] = []
        var current = ""
        var inWord = false
        var quote: Character?
        var escaped = false
        for character in trimmed {
            if escaped {
                current.append(character)
                escaped = false
            } else if character == "\\" && quote != "'" {
                escaped = true
                inWord = true
            } else if let open = quote {
                if character == open { quote = nil } else { current.append(character) }
            } else if character == "\"" || character == "'" {
                quote = character
                inWord = true
            } else if character.isWhitespace {
                if inWord { words.append(current) }
                current = ""
                inWord = false
            } else {
                current.append(character)
                inWord = true
            }
        }
        if inWord { words.append(current) }
        return words
    }

    // MARK: Reading

    private static func readKeyMaterial(directive: String, path: String, directory: URL) throws -> String {
        let url = try resolveInside(path, directive: directive, directory: directory)
        let data = try readText(at: url, maxSize: maxReferencedFileSize)
        let text = String(decoding: data, as: UTF8.self)
        guard text.contains("-----BEGIN") else { throw Failure.notKeyMaterial(directive: directive, path: url.path) }
        return text.replacingOccurrences(of: "\r\n", with: "\n").trimmingCharacters(in: .whitespacesAndNewlines)
    }

    /// Like openvpn, the user name is the first line and the password the second.
    private static func readCredentials(path: String, directory: URL) throws -> Credentials {
        let url = try resolveInside(path, directive: "auth-user-pass", directory: directory)
        let data = try readText(at: url, maxSize: maxReferencedFileSize)
        let lines = String(decoding: data, as: UTF8.self).split(omittingEmptySubsequences: false, whereSeparator: \.isNewline)
        guard lines.count >= 2, !lines[0].isEmpty, !lines[1].isEmpty else { throw Failure.notCredentials(path: url.path) }
        return Credentials(username: String(lines[0]), password: String(lines[1]))
    }

    /// The file `path` names, symlinks resolved, when it is inside the profile's
    /// directory. openvpn takes paths relative to the profile, and nothing else
    /// is read here: the content goes to a root daemon, so a profile must not be
    /// able to pick up any other file of the user's (`key ~/.ssh/id_ed25519`).
    private static func resolveInside(_ path: String, directive: String, directory: URL) throws -> URL {
        let outside = Failure.outsideProfileDirectory(directive: directive, path: path)
        guard !path.hasPrefix("/"), !path.hasPrefix("~") else { throw outside }

        // `..` is resolved by name first, so that a file that does not exist is
        // not reported as missing when the profile asked for it outside.
        var components: [Substring] = []
        for part in path.split(separator: "/") {
            switch part {
            case ".": continue
            case "..": guard components.popLast() != nil else { throw outside }
            default: components.append(part)
            }
        }

        do {
            let root = try resolvingSymlinks(directory.path)
            let rootPrefix = root.hasSuffix("/") ? root : root + "/"
            let named = ([root] + components.map(String.init)).joined(separator: "/")
            let resolved: String
            do {
                resolved = try resolvingSymlinks(named)
            } catch let error as POSIXError where error.code == .ENOENT {
                throw Failure.missingFile(directive: directive, path: named)
            }
            guard resolved.hasPrefix(rootPrefix) else { throw outside }
            return URL(fileURLWithPath: resolved)
        } catch let failure as Failure {
            throw failure
        } catch {
            throw Failure.unreadable(path: path, reason: error.localizedDescription)
        }
    }

    private static func resolvingSymlinks(_ path: String) throws -> String {
        guard let resolved = realpath(path, nil) else { throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO) }
        defer { free(resolved) }
        return String(cString: resolved)
    }

    private static func readText(at url: URL, maxSize: Int) throws -> Data {
        // A profile decides which files get read: a device such as /dev/zero never ends.
        let values: URLResourceValues
        do {
            values = try url.resolvingSymlinksInPath().resourceValues(forKeys: [.isRegularFileKey, .fileSizeKey])
        } catch {
            throw Failure.unreadable(path: url.path, reason: error.localizedDescription)
        }
        guard values.isRegularFile == true else { throw Failure.notText(path: url.path) }
        guard (values.fileSize ?? 0) <= maxSize else { throw Failure.tooLarge(path: url.path) }

        let data: Data
        do {
            data = try Data(contentsOf: url)
        } catch {
            throw Failure.unreadable(path: url.path, reason: error.localizedDescription)
        }
        guard data.count <= maxSize else { throw Failure.tooLarge(path: url.path) }
        guard String(data: data, encoding: .utf8) != nil, !data.contains(0) else { throw Failure.notText(path: url.path) }
        return data
    }
}
