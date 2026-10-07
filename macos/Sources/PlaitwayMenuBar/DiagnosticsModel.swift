import Foundation
import Observation
import PlaitwayClient

/// What the Diagnostics page shows, read from the daemon while the page is open.
@MainActor
@Observable
final class DiagnosticsModel {
    private(set) var diagnostics: Diagnostics?
    /// The latest read or command failed; the previous reading stays on screen.
    private(set) var failure: String?

    private static let refreshInterval: Duration = .seconds(2)

    /// Reads until the task is cancelled: routes change with connections and
    /// network changes, neither of which the profile stream reports.
    func run(store: ProfileStore) async {
        while !Task.isCancelled {
            await refresh(store: store)
            try? await Task.sleep(for: Self.refreshInterval)
        }
    }

    func refresh(store: ProfileStore) async {
        do {
            let reading = try await store.fetchDiagnostics()
            // Most readings equal the last one; assigning would redraw the page for nothing.
            if reading != diagnostics { diagnostics = reading }
            if failure != nil { failure = nil }
        } catch {
            if !Task.isCancelled { failure = userMessage(for: error) }
        }
    }
}
