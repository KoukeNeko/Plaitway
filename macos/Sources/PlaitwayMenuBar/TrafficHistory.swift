import Foundation
import Observation
import PlaitwayClient

/// How fast data moves through each connected profile, worked out here from the byte counters
/// the daemon reports every couple of seconds; the daemon keeps no history.
@MainActor
@Observable
final class TrafficHistory {
    struct Sample: Equatable, Identifiable {
        let id: Int
        let date: Date
        /// Bytes per second.
        let received: Double
        let sent: Double
    }

    /// Two minutes at the daemon's pace.
    static let capacity = 60
    /// A reading closer than this to the last one is a second event for the same counters.
    private static let minimumInterval: TimeInterval = 0.9
    /// The rate shown is the mean of the last few samples; one reading jumps too much to read.
    private static let smoothing = 3

    private(set) var samples: [String: [Sample]] = [:]
    @ObservationIgnored private var counters: [String: (date: Date, received: UInt64, sent: UInt64)] = [:]
    @ObservationIgnored private var nextID = 0

    func record(_ profiles: [Profile], at now: Date = .now) {
        let connected = profiles.filter { $0.state == .connected }
        for id in counters.keys where !connected.contains(where: { $0.id == id }) {
            // A reconnection starts the counters again, and so the graph.
            counters[id] = nil
            samples[id] = nil
        }
        for profile in connected {
            let received = profile.status.rxBytes
            let sent = profile.status.txBytes
            guard let previous = counters[profile.id] else {
                counters[profile.id] = (now, received, sent)
                continue
            }
            let elapsed = now.timeIntervalSince(previous.date)
            // The reading stays as it was, so that the next one has a full interval to measure.
            guard elapsed >= Self.minimumInterval else { continue }
            counters[profile.id] = (now, received, sent)
            // A counter that went down was reset: the interval says nothing.
            guard received >= previous.received, sent >= previous.sent else {
                samples[profile.id] = nil
                continue
            }
            nextID += 1
            var list = samples[profile.id] ?? []
            list.append(Sample(
                id: nextID,
                date: now,
                received: Double(received - previous.received) / elapsed,
                sent: Double(sent - previous.sent) / elapsed
            ))
            if list.count > Self.capacity { list.removeFirst(list.count - Self.capacity) }
            samples[profile.id] = list
        }
    }

    /// The current rates; nil until there are two readings.
    func rate(for profileID: String) -> (received: Double, sent: Double)? {
        guard let recent = samples[profileID]?.suffix(Self.smoothing), !recent.isEmpty else { return nil }
        let count = Double(recent.count)
        return (recent.reduce(0) { $0 + $1.received } / count, recent.reduce(0) { $0 + $1.sent } / count)
    }
}
