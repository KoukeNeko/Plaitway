import Foundation
import Observation
import PlaitwayClient

/// The live tail of one log: a profile's, or the daemon's own for an empty id.
@MainActor
@Observable
final class LogTail {
    struct Entry: Identifiable, Equatable {
        let id: Int
        let date: Date?
        let level: LogLevel
        let text: String

        /// The line as it is copied: "12:00:01 INFO text".
        var plainText: String {
            [date.map(Formatting.logTime), level.tag, text].compactMap { $0 }.joined(separator: " ")
        }
    }

    private(set) var entries: [Entry] = []

    @ObservationIgnored private var nextID = 0
    /// The stream is new and its first line has not come: the lines shown are the old stream's.
    @ObservationIgnored private var replacesEntries = false

    /// Lines kept; the daemon keeps 1000 itself, so this only bounds a long session.
    static let capacity = 2000
    private static let retryInterval: Duration = .seconds(1)
    /// Lines that arrive within this of each other are shown together. The buffered tail comes as
    /// one burst, and a page that is drawn again for each line of it takes seconds to settle.
    private static let batchInterval: Duration = .milliseconds(50)

    @ObservationIgnored private var pending: [LogLine] = []
    @ObservationIgnored private var flush: Task<Void, Never>?

    var plainText: String {
        entries.map(\.plainText).joined(separator: "\n")
    }

    /// Follows the log until the task is cancelled, and again after the daemon
    /// restarts. A profile that no longer exists ends it.
    func run(store: ProfileStore, profileID: String) async {
        while !Task.isCancelled {
            // A new stream starts with the buffered tail again: what is shown stays until its first
            // line arrives, so that a helper restart does not empty the log for the seconds it takes.
            replacesEntries = true
            do {
                for try await line in store.logs(profileID: profileID) {
                    enqueue(line)
                }
            } catch {
                if DaemonFailure(error) == .notFound { return }
            }
            try? await Task.sleep(for: Self.retryInterval)
        }
    }

    func enqueue(_ line: LogLine) {
        pending.append(line)
        guard flush == nil else { return }
        flush = Task { [weak self] in
            try? await Task.sleep(for: Self.batchInterval)
            self?.flushPending()
        }
    }

    private func flushPending() {
        flush = nil
        let lines = pending
        pending = []
        for line in lines { append(line) }
    }

    func append(_ line: LogLine) {
        if replacesEntries {
            entries = []
            replacesEntries = false
        }
        entries.append(Entry(id: nextID, date: line.date, level: line.level, text: line.text))
        nextID += 1
        // Trim in batches so that a full buffer does not shift on every line.
        if entries.count > Self.capacity + 200 {
            entries.removeFirst(entries.count - Self.capacity)
        }
    }
}
