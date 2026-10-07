import Foundation
import Observation
import PlaitwayClient

/// The text of one profile while it is edited: what the editor shows, what the daemon holds,
/// and what the daemon said about the last attempt to store it. It lives in the app model, so
/// that a half-made edit survives a visit to another profile.
///
/// The secrets (private keys, inline key blocks) are kept off the screen: the editor shows a
/// placeholder in their place until the person asks to see them, and `SecretMask` puts them
/// back at save time.
@MainActor
@Observable
final class ProfileEditor {
    enum Phase: Equatable {
        case loading
        case ready
        /// The text cannot be read: the person is not an administrator, or the daemon is away.
        case unavailable(String)
    }

    let profileID: String
    let kind: ProfileKind

    private(set) var phase: Phase = .loading
    /// What the editor shows and the person changes.
    var text = ""
    private(set) var showsSecrets = false
    /// What the daemon refused in the last save; its line is a line of `text`.
    private(set) var diagnostic: ConfigDiagnostic?
    /// What the daemon changed in the text it stored.
    private(set) var warnings: [ImportWarning] = []
    /// The last save did not restart the profile, which is on: it runs with the old text.
    private(set) var runsOldText = false
    private(set) var isSaving = false

    /// The text the daemon holds, secrets included.
    private var stored = ""
    private var mask: SecretMask

    init(profileID: String, kind: ProfileKind) {
        self.profileID = profileID
        self.kind = kind
        mask = SecretMask(text: "", kind: kind)
    }

    /// The text with the secrets back in it; nil while a placeholder stands twice.
    private var restoredText: String? {
        showsSecrets ? text : (try? mask.restore(text))?.text
    }

    /// Whether the person changed something. A text that cannot be restored is changed by definition.
    var isDirty: Bool {
        phase == .ready && restoredText != stored
    }

    /// Reads the profile text, unless there are edits that a read would throw away.
    func load(from store: ProfileStore) async {
        guard !isDirty else { return }
        do {
            stored = try await store.profileContent(id: profileID)
            diagnostic = nil
            show(stored)
            phase = .ready
        } catch {
            // A read that was cancelled by leaving the page says nothing; the next visit reads again.
            guard !Task.isCancelled else { return }
            // An edit in progress stays on screen; a failed first read says why.
            if phase != .ready { phase = .unavailable(userMessage(for: error)) }
        }
    }

    /// Shows the secrets, or hides them again with whatever the person typed in the meantime.
    func toggleSecrets() {
        guard phase == .ready else { return }
        // A mark is a line of the text as it was shown, and showing or hiding a key block changes the lines.
        diagnostic = nil
        if showsSecrets {
            mask = SecretMask(text: text, kind: kind)
            text = mask.displayText
            showsSecrets = false
        } else {
            do {
                text = try mask.restore(text).text
                showsSecrets = true
            } catch SecretMask.Failure.duplicatedPlaceholder(let number, let line) {
                // A secret cannot be shown in two places.
                diagnostic = Self.duplicate(number, line)
            } catch {
                diagnostic = ConfigDiagnostic(line: nil, message: userMessage(for: error))
            }
        }
    }

    /// Hides the secrets again, with the edits made while they were shown. The editor outlives the
    /// page, and the keys must not be on screen when the person comes back to it.
    func hideSecrets() {
        if showsSecrets { toggleSecrets() }
    }

    /// The profile connected again: it runs the text that is stored now.
    func noteRestart() {
        runsOldText = false
    }

    func revert() {
        show(stored)
        diagnostic = nil
    }

    /// Stores the text. Returns whether the daemon took it; when it refused the text, `diagnostic`
    /// says why. Any other failure is thrown.
    /// - Parameters:
    ///   - reconnect: restarts the profile with the new text.
    ///   - isOn: the profile is switched on, so that it keeps running with the old text unless restarted.
    func save(to store: ProfileStore, reconnect: Bool, isOn: Bool) async throws -> Bool {
        guard phase == .ready, !isSaving else { return false }
        let restoration: SecretMask.Restoration?
        let content: String
        if showsSecrets {
            restoration = nil
            content = text
        } else {
            do {
                let restored = try mask.restore(text)
                restoration = restored
                content = restored.text
            } catch SecretMask.Failure.duplicatedPlaceholder(let number, let line) {
                diagnostic = Self.duplicate(number, line)
                return false
            }
        }

        isSaving = true
        defer { isSaving = false }
        do {
            let result = try await store.updateProfileContent(id: profileID, content: content, reconnect: reconnect)
            warnings = result.warnings
            runsOldText = isOn && !reconnect
            diagnostic = nil
            // The daemon may have stripped something from what it stored: show what it holds.
            stored = (try? await store.profileContent(id: profileID)) ?? content
            show(stored)
            return true
        } catch {
            guard let rejection = ConfigDiagnostic(error) else { throw error }
            diagnostic = ConfigDiagnostic(
                line: rejection.line.map { restoration?.displayLine(forRestoredLine: $0) ?? $0 },
                message: rejection.message
            )
            return false
        }
    }

    /// Shows `restored` as the editor's text, with the secrets hidden unless they are shown.
    private func show(_ restored: String) {
        if showsSecrets {
            text = restored
        } else {
            mask = SecretMask(text: restored, kind: kind)
            text = mask.displayText
        }
    }

    private static func duplicate(_ number: Int, _ line: Int) -> ConfigDiagnostic {
        ConfigDiagnostic(line: line, message: String(localized: "Secret \(number) appears more than once", bundle: .module))
    }
}
