import PlaitwayClient
import SwiftUI

struct CredentialSheet: View {
    let prompt: CredentialPrompt
    let profileName: String
    @Environment(AppModel.self) private var model
    @State private var username = ""
    @State private var password = ""
    @FocusState private var focus: Field?

    private enum Field { case username, password }

    private var asksForUsername: Bool { prompt.kind != .keyPassphrase }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            VStack(alignment: .leading, spacing: 2) {
                (asksForUsername ? Text("Credentials", bundle: .module) : Text("Key passphrase", bundle: .module))
                    .font(.headline)
                Text(verbatim: profileName).foregroundStyle(.secondary)
            }
            if prompt.rejected {
                Group {
                    if prompt.message.isEmpty {
                        Text("Credentials rejected", bundle: .module)
                    } else {
                        Text(verbatim: prompt.message)
                    }
                }
                .foregroundStyle(.red)
                .accessibilityIdentifier("credentials.rejected")
            }
            Form {
                if asksForUsername {
                    TextField(text: $username) { Text("Username", bundle: .module) }
                        .focused($focus, equals: .username)
                        .accessibilityIdentifier("credentials.username")
                }
                SecureField(text: $password) {
                    asksForUsername ? Text("Password", bundle: .module) : Text("Passphrase", bundle: .module)
                }
                .focused($focus, equals: .password)
                .accessibilityIdentifier("credentials.password")
            }
            .onSubmit(submit)
            HStack {
                Spacer()
                Button(role: .cancel) {
                    Task { await model.cancelCredentials(profileID: prompt.profileID) }
                } label: { Text("Cancel", bundle: .module) }
                    .keyboardShortcut(.cancelAction)
                    .accessibilityIdentifier("credentials.cancel")
                Button(action: submit) { Text("Connect", bundle: .module) }
                    .keyboardShortcut(.defaultAction)
                    .disabled(!canSubmit)
                    .accessibilityIdentifier("credentials.submit")
            }
        }
        .padding(20)
        .frame(width: 380)
        .interactiveDismissDisabled()
        .onAppear { focus = asksForUsername ? .username : .password }
    }

    private var canSubmit: Bool {
        !password.isEmpty && (!asksForUsername || !username.isEmpty)
    }

    private func submit() {
        guard canSubmit else { return }
        let id = prompt.profileID
        let user = asksForUsername ? username : ""
        let secret = password
        // The sheet goes away with the prompt; do not keep the secret in view state.
        password = ""
        Task { await model.submitCredentials(profileID: id, username: user, password: secret) }
    }
}

struct ImportReportSheet: View {
    let report: ImportReport
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Import", bundle: .module).font(.headline)
            List(report.outcomes) { outcome in
                OutcomeRow(outcome: outcome)
            }
            .listStyle(.inset)
            .frame(minHeight: 140)
            .accessibilityIdentifier("import.list")
            HStack {
                Spacer()
                Button { dismiss() } label: { Text("Close", bundle: .module) }
                    .keyboardShortcut(.defaultAction)
                    .keyboardShortcut(.cancelAction)
                    .accessibilityIdentifier("import.close")
            }
        }
        .padding(20)
        .frame(width: 520, height: 340)
    }
}

private struct OutcomeRow: View {
    let outcome: ImportReport.Outcome

    var body: some View {
        switch outcome.result {
        case .imported(let name, let warnings):
            VStack(alignment: .leading, spacing: 4) {
                Label {
                    Text(verbatim: outcome.filename)
                } icon: {
                    Image(systemName: warnings.isEmpty ? "checkmark.circle.fill" : "exclamationmark.triangle.fill")
                        .foregroundStyle(warnings.isEmpty ? Color.green : Color.orange)
                }
                Text(verbatim: name).font(.caption).foregroundStyle(.secondary)
                ForEach(Array(warnings.enumerated()), id: \.offset) { _, warning in
                    Text(verbatim: warning.summary).font(.caption).foregroundStyle(.secondary)
                }
            }
            .accessibilityElement(children: .combine)
            .accessibilityIdentifier("import.outcome.\(outcome.filename)")
        case .failed(let message):
            VStack(alignment: .leading, spacing: 4) {
                Label {
                    Text(verbatim: outcome.filename)
                } icon: {
                    Image(systemName: "xmark.octagon.fill").foregroundStyle(.red)
                }
                // The reason is what the user acts on: full size, and selectable to be copied.
                Text(verbatim: message).textSelection(.enabled)
            }
            .accessibilityElement(children: .combine)
            .accessibilityIdentifier("import.outcome.\(outcome.filename)")
        }
    }
}
