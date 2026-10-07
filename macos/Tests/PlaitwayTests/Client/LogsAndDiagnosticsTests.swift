import Foundation
import GRPCCore
import PlaitwayClient
import Testing

@MainActor
struct LogsAndDiagnosticsTests {
    // MARK: logs

    @Test func tailsAProfilesLog() async throws {
        try await withDaemon { _, store in
            let id = try await store.importFixture("office.ovpn")
            try await store.setEnabled(true, profileID: id)

            let stream = store.logs(profileID: id)
            let line = try await firstLine(in: stream) { $0.text.hasPrefix("connected on") }
            #expect(line.profileID == id)
            #expect(line.level == .info)
            #expect(line.hasTime)
        }
    }

    @Test func streamsLiveLinesAfterTheBufferedTail() async throws {
        try await withDaemon { _, store in
            let id = try await store.importFixture("office.ovpn")
            try await store.setEnabled(true, profileID: id)
            try await store.waitForState(id, .connected)

            let stream = store.logs(profileID: id)
            _ = try await firstLine(in: stream) { $0.text.hasPrefix("connected on") }

            // A line that did not exist when the stream was opened.
            try await store.setEnabled(false, profileID: id)
            let stopping = try await firstLine(in: stream) { $0.text == "stopping" }
            #expect(stopping.profileID == id)
        }
    }

    @Test func tailsTheDaemonsOwnLogWithAnEmptyProfileID() async throws {
        try await withDaemon { _, store in
            let stream = store.logs(profileID: "")
            let line = try await firstLine(in: stream) { $0.text.contains("listening") }
            #expect(line.profileID.isEmpty)
        }
    }

    @Test func aLogOfAnUnknownProfileIsNotFound() async throws {
        try await withDaemon { _, store in
            do {
                _ = try await firstLine(in: store.logs(profileID: "nope")) { _ in true }
                Issue.record("an unknown profile id was accepted")
            } catch let error as RPCError {
                #expect(error.code == .notFound)
            }
        }
    }

    // MARK: diagnostics

    @Test func describesTheNetworkAndTheStaleRoute() async throws {
        try await withDaemon { _, store in
            // The store reads the daemon's version in the background after it connects.
            try await waitUntil("the daemon's version") { store.daemonInfo != nil }
            let diagnostics = try await store.fetchDiagnostics()
            #expect(diagnostics.network.defaultGatewayV4 == "192.168.0.1")
            #expect(diagnostics.network.defaultInterfaceV4 == "en0")
            #expect(diagnostics.network.interfaces.contains("en0"))
            #expect(diagnostics.ownedRoutes.isEmpty)
            #expect(diagnostics.daemon.version == store.daemonInfo?.version)

            let stale = try #require(diagnostics.staleRoutes.first)
            #expect(diagnostics.staleRoutes.count == 1)
            #expect(stale.key == "fake-stale-1")
            #expect(stale.prefix == "203.0.113.9/32")
            #expect(stale.gateway == "192.168.0.254")
            #expect(stale.interface == "en0")
            #expect(!stale.reason.isEmpty)
        }
    }

    @Test func listsTheRoutesTheDaemonOwnsWithTheirStates() async throws {
        try await withDaemon { _, store in
            let id = try await store.importFixture("office.ovpn", Fixture.openVPN(markers: ["# fake: conflict"]))
            try await store.setEnabled(true, profileID: id)
            try await store.waitForState(id, .connected)

            let diagnostics = try await store.fetchDiagnostics()
            let states = Dictionary(uniqueKeysWithValues: diagnostics.ownedRoutes.map { ($0.prefix, $0) })
            #expect(states["192.168.1.0/24"]?.state == .installed)
            #expect(states["192.168.1.0/24"]?.kind == .tunnel)
            #expect(states["192.168.1.0/24"]?.owner == id)
            #expect(states["10.99.0.0/16"]?.state == .shadowed)
            #expect(states["192.168.0.0/24"]?.state == .blocked)
            #expect(diagnostics.resolverEntries.contains { $0.contains(id) })

            // The profile's own view of the same routes names who shadows what.
            let routes = try #require(store.profile(id)?.status.routes)
            let shadowed = try #require(routes.first { $0.prefix == "10.99.0.0/16" })
            #expect(shadowed.state == .shadowed)
            #expect(shadowed.shadowedBy == "fake-peer")
            #expect(routes.first { $0.prefix == "192.168.0.0/24" }?.state == .blocked)
        }
    }

    @Test func removesAStaleRouteOnce() async throws {
        try await withDaemon { _, store in
            try await store.removeStaleRoute(key: "fake-stale-1")
            #expect(try await store.fetchDiagnostics().staleRoutes.isEmpty)

            do {
                try await store.removeStaleRoute(key: "fake-stale-1")
                Issue.record("a route was removed twice")
            } catch let error as RPCError {
                #expect(error.code == .notFound)
            }
        }
    }

    @Test func resyncRebuildsWhatTheDaemonOwns() async throws {
        try await withDaemon { _, store in
            let id = try await store.importFixture("office.ovpn")
            try await store.setEnabled(true, profileID: id)
            try await store.waitForState(id, .connected)
            try await store.removeStaleRoute(key: "fake-stale-1")

            try await store.resync()

            // The fake daemon reports the stale route again after a resync, and
            // the connected profile reconnects for a moment.
            let diagnostics = try await store.fetchDiagnostics()
            #expect(diagnostics.network.lastChangeReason == "manual")
            #expect(diagnostics.staleRoutes.map(\.key) == ["fake-stale-1"])
            try await store.waitForState(id, .reconnecting)
            try await store.waitForState(id, .connected)
        }
    }
}
