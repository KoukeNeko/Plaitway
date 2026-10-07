import PlaitwayClient
import SwiftUI

/// The window's content: the helper's setup while it is not ready, the profiles
/// otherwise, and the sheets and dialogs that can appear over either.
struct RootView: View {
    @Environment(AppModel.self) private var model
    @State private var isDropTargeted = false

    var body: some View {
        @Bindable var model = model

        Group {
            if let content = model.setup.content, !model.keepsProfilesInView {
                SetupView(content: content)
            } else {
                MainView()
            }
        }
        .frame(minWidth: 860, minHeight: 540)
        .overlay {
            if isDropTargeted {
                RoundedRectangle(cornerRadius: 8).strokeBorder(Color.accentColor, lineWidth: 3).padding(4).allowsHitTesting(false)
            }
        }
        .dropDestination(for: URL.self) { urls, _ in
            Task { await model.importFiles(urls) }
            return true
        } isTargeted: { isDropTargeted = $0 }
        .sheet(item: credentialPrompt) { prompt in
            CredentialSheet(prompt: prompt, profileName: model.profileName(prompt.profileID) ?? prompt.profileID)
                // Another profile's prompt is a new sheet: its fields start empty.
                .id(prompt.profileID)
        }
        .sheet(item: $model.importReport) { report in
            ImportReportSheet(report: report)
        }
        .modifier(AlertPresenter())
        .confirmationDialog(
            Text("Delete “\(deletionName)”?", bundle: .module),
            isPresented: Binding(get: { model.pendingDeletion != nil }, set: { if !$0 { model.pendingDeletion = nil } }),
            titleVisibility: .visible,
            // The dialog clears pendingDeletion before its button runs; the id travels with the presentation.
            presenting: model.pendingDeletion
        ) { profileID in
            Button(role: .destructive) {
                Task { await model.delete(profileID: profileID) }
            } label: { Text("Delete", bundle: .module) }
            Button(role: .cancel) { } label: { Text("Cancel", bundle: .module) }
        } message: { _ in
            Text("The profile and its saved credentials are removed.", bundle: .module)
        }
        // Here and not on the General page: the version banner asks from every page.
        .confirmationDialog(
            Text("Reinstall helper?", bundle: .module),
            isPresented: $model.isConfirmingReinstall,
            titleVisibility: .visible
        ) {
            Button(role: .destructive) {
                Task { await model.reinstallHelper() }
            } label: { Text("Reinstall", bundle: .module) }
            Button(role: .cancel) { } label: { Text("Cancel", bundle: .module) }
        } message: {
            Text("Connected profiles disconnect.", bundle: .module)
        }
    }

    private var deletionName: String {
        model.pendingDeletion.flatMap(model.profileName) ?? ""
    }

    /// The sheet is answered with its own buttons; a dismissal by the system is not a decision.
    private var credentialPrompt: Binding<CredentialPrompt?> {
        Binding(get: { model.store.credentialPrompts.first }, set: { _ in })
    }
}

/// The alert for what a command did not manage. Every window that can start a command presents it:
/// an alert nobody can see is no report.
struct AlertPresenter: ViewModifier {
    @Environment(AppModel.self) private var model

    func body(content: Content) -> some View {
        content.alert(
            Text(verbatim: model.alert?.title ?? ""),
            isPresented: Binding(get: { model.alert != nil }, set: { if !$0 { model.alert = nil } }),
            presenting: model.alert
        ) { _ in
            Button { } label: { Text("Close", bundle: .module) }
        } message: { alert in
            Text(verbatim: alert.message)
        }
    }
}

/// The helper is not ready: one thing to do about it.
struct SetupView: View {
    let content: SetupContent
    @Environment(AppModel.self) private var model

    var body: some View {
        ContentUnavailableView {
            Label { Text(verbatim: content.title).accessibilityIdentifier("setup.title") } icon: { Image(systemName: content.symbol) }
        } description: {
            // The detail may be a path or a command the user wants to copy.
            if let detail = content.detail {
                Text(verbatim: detail).textSelection(.enabled)
            }
            if content.showsProgress {
                ProgressView().controlSize(.small)
            }
        } actions: {
            if let primary = content.primary {
                Button { Task { await model.perform(primary) } } label: { Text(verbatim: primary.label) }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.large)
                    .accessibilityIdentifier("setup.primary")
            }
            if let secondary = content.secondary {
                Button { Task { await model.perform(secondary) } } label: { Text(verbatim: secondary.label) }
                    .buttonStyle(.link)
                    .accessibilityIdentifier("setup.secondary")
            }
        }
        .navigationTitle(Text(verbatim: ""))
    }
}

/// A note above a page, in the content layer: something the person may want to act on.
struct NoticeBar<Content: View>: View {
    var symbol = "exclamationmark.triangle.fill"
    var tint: Color = .orange
    let identifier: String
    @ViewBuilder let content: Content

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            Image(systemName: symbol).foregroundStyle(tint).accessibilityHidden(true)
            content
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(12)
        .background(tint.opacity(0.12), in: .rect(cornerRadius: 10))
        .padding([.horizontal, .top], 12)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier(identifier)
    }
}

/// The helper runs, but it is not the app's version.
struct VersionBanner: View {
    let daemon: String
    let app: String
    @Environment(AppModel.self) private var model

    var body: some View {
        NoticeBar(identifier: "banner.version") {
            Text("Helper out of date", bundle: .module)
            Text(verbatim: "\(daemon) → \(app)").foregroundStyle(.secondary).monospacedDigit()
            Spacer(minLength: 12)
            if model.isHelperExternal {
                // scripts/dev-install-daemon.sh installed it: only the script replaces it.
                Text("Run sudo scripts/dev-install-daemon.sh again.", bundle: .module)
                    .textSelection(.enabled)
                    .accessibilityIdentifier("banner.externalHint")
            } else {
                Button { model.isConfirmingReinstall = true } label: { Text("Reinstall Helper", bundle: .module) }
                    .accessibilityIdentifier("banner.reinstall")
            }
        }
    }
}

/// The helper stopped answering while the window shows profiles: they stay, and the way back is here.
struct HelperUnavailableBanner: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        NoticeBar(identifier: "banner.unavailable") {
            Text("Helper unavailable", bundle: .module)
            Spacer(minLength: 12)
            Button { model.retry() } label: { Text("Retry", bundle: .module) }
                .accessibilityIdentifier("banner.retry")
        }
    }
}
