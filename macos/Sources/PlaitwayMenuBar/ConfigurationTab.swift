import PlaitwayClient
import SwiftUI

/// The profile's own text, to read and to change: the file the daemon keeps for it, with the
/// secrets hidden until asked for.
struct ConfigurationTab: View {
    let profile: Profile
    @Environment(AppModel.self) private var model

    var body: some View {
        @Bindable var editor = model.editor(for: profile)

        VStack(spacing: 0) {
            PageBar {
                Button { editor.revert() } label: { Text("Revert", bundle: .module) }
                    .disabled(!editor.isDirty)
                    .accessibilityIdentifier("configuration.revert")
                Button { editor.toggleSecrets() } label: {
                    Label {
                        Text(editor.showsSecrets ? "Hide Secrets" : "Show Secrets", bundle: .module)
                    } icon: {
                        Image(systemName: editor.showsSecrets ? "eye.slash" : "eye")
                    }
                }
                .help(Text(editor.showsSecrets ? "Hide Secrets" : "Show Secrets", bundle: .module))
                .disabled(editor.phase != .ready)
                .accessibilityIdentifier("configuration.secrets")
                Spacer()
                SaveButton(profile: profile, editor: editor)
            }
            switch editor.phase {
            case .loading:
                ProgressView().controlSize(.small).frame(maxWidth: .infinity, maxHeight: .infinity)
            case .unavailable(let message):
                ContentUnavailableView {
                    Label { Text(verbatim: message) } icon: { Image(systemName: "lock") }
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            case .ready:
                Notices(editor: editor)
                ConfigEditorView(text: $editor.text, kind: profile.kind, markedLine: editor.diagnostic?.line, isEditable: !editor.isSaving)
            }
        }
        // The bar stays at the top whatever is below it.
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        .task(id: profile.id) { await editor.load(from: model.store) }
        .onDisappear { editor.hideSecrets() }
        .onChange(of: profile.status.connectedSince) { editor.noteRestart() }
    }
}

private struct SaveButton: View {
    let profile: Profile
    let editor: ProfileEditor
    @Environment(AppModel.self) private var model

    var body: some View {
        // A running profile keeps the text it started with: the person says whether to restart it.
        if profile.desiredEnabled {
            Menu {
                Button { save(reconnect: true) } label: { Text("Save and Reconnect", bundle: .module) }
                    .accessibilityIdentifier("configuration.saveAndReconnect")
            } label: {
                Text("Save", bundle: .module)
            } primaryAction: {
                save(reconnect: false)
            }
            .keyboardShortcut("s", modifiers: .command)
            .disabled(!canSave)
            .accessibilityIdentifier("configuration.save")
        } else {
            Button { save(reconnect: false) } label: { Text("Save", bundle: .module) }
                .keyboardShortcut("s", modifiers: .command)
                .disabled(!canSave)
                .accessibilityIdentifier("configuration.save")
        }
    }

    private var canSave: Bool {
        editor.isDirty && !editor.isSaving
    }

    private func save(reconnect: Bool) {
        Task {
            do {
                _ = try await editor.save(to: model.store, reconnect: reconnect, isOn: profile.desiredEnabled)
            } catch {
                model.report(error, as: .saveFailed)
            }
        }
    }
}

/// What the daemon said about the text, above the editor.
private struct Notices: View {
    let editor: ProfileEditor

    var body: some View {
        if let diagnostic = editor.diagnostic {
            NoticeBar(symbol: "xmark.octagon.fill", tint: .red, identifier: "configuration.diagnostic") {
                if let line = diagnostic.line {
                    Text("Line \(line)", bundle: .module).monospacedDigit()
                }
                Text(verbatim: diagnostic.message).textSelection(.enabled)
            }
        }
        if !editor.warnings.isEmpty && !editor.isDirty {
            NoticeBar(identifier: "configuration.warnings") {
                VStack(alignment: .leading, spacing: 2) {
                    ForEach(Array(editor.warnings.enumerated()), id: \.offset) { _, warning in
                        Text(verbatim: warning.summary).textSelection(.enabled)
                    }
                }
            }
        }
        if editor.runsOldText && !editor.isDirty {
            NoticeBar(symbol: "info.circle.fill", tint: .secondary, identifier: "configuration.appliesLater") {
                Text("Applies at next connection", bundle: .module)
            }
        }
    }
}
