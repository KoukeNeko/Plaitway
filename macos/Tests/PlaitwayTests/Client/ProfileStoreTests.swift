import Foundation
import GRPCCore
import PlaitwayClient
import Testing

@MainActor
struct ProfileStoreTests {
    @Test func startsEmptyAndKnowsTheDaemon() async throws {
        try await withDaemon { _, store in
            #expect(store.profiles.isEmpty)
            try await waitUntil("the daemon's description") { store.daemonInfo != nil }
            #expect(store.daemonInfo?.privileged == true)
            #expect(store.daemonInfo?.engines.map(\.available) == [true, true])
            #expect(store.daemonInfo?.version.isEmpty == false)
        }
    }

    @Test func connectsSeveralProfilesAtTheSameTime() async throws {
        try await withDaemon { _, store in
            let office = try await store.importFixture("office.ovpn")
            let lab = try await store.importFixture("lab.ovpn")
            let home = try await store.importFixture("home.conf", Fixture.wireGuard())

            async let first: Void = store.setEnabled(true, profileID: office)
            async let second: Void = store.setEnabled(true, profileID: lab)
            async let third: Void = store.setEnabled(true, profileID: home)
            _ = try await (first, second, third)

            try await waitUntil("three connected profiles") {
                [office, lab, home].allSatisfy { store.profile($0)?.state == .connected }
            }
            #expect(store.profile(home)?.status.interfaceName.hasPrefix("utun") == true)
        }
    }

    @Test func reportsFailureWithAnError() async throws {
        try await withDaemon { _, store in
            let broken = try await store.importFixture("broken.ovpn", Fixture.openVPN(markers: ["# fake: fail"]))

            try await store.setEnabled(true, profileID: broken)
            try await store.waitForState(broken, .failed)
            #expect(store.profile(broken)?.lastError.isEmpty == false)
        }
    }

    @Test func disablingDisconnects() async throws {
        try await withDaemon { _, store in
            let office = try await store.importFixture("office.ovpn")

            try await store.setEnabled(true, profileID: office)
            try await store.waitForState(office, .connected)
            try await store.setEnabled(false, profileID: office)
            try await store.waitForState(office, .disconnected)
            #expect(store.profile(office)?.desiredEnabled == false)
        }
    }

    @Test func unknownProfileIsNotFound() async throws {
        try await withDaemon { _, store in
            do {
                try await store.setEnabled(true, profileID: "nope")
                Issue.record("an unknown profile id was accepted")
            } catch let error as RPCError {
                #expect(error.code == .notFound)
                #expect(DaemonFailure(error) == .notFound)
            }
        }
    }

    @Test func recoversWhenTheDaemonRestarts() async throws {
        try await withDaemon { daemon, store in
            let office = try await store.importFixture("office.ovpn")
            try await store.setEnabled(true, profileID: office)
            try await store.waitForState(office, .connected)

            daemon.kill()
            try await waitUntil("the outage to be noticed") { store.connection == .unavailable }
            // The store keeps the last known profiles instead of emptying the UI.
            #expect(store.profile(office)?.state == .connected)

            try await daemon.start()
            try await waitUntil("the store to reconnect") { store.connection == .connected }
            // The profile survives in the state directory; its tunnel does not.
            #expect(store.profile(office)?.state == .disconnected)
            #expect(store.profile(office)?.desiredEnabled == false)
        }
    }

    @Test func launchingTheAppDoesNotReconnectWhatTheUserDisconnected() async throws {
        try await withDaemon { daemon, store in
            var autoConnect = ProfileSettings()
            autoConnect.autoConnect = true
            let flagged = try await store.importFixture("flagged.ovpn", settings: autoConnect)
            // The daemon connects auto-connect profiles when it starts; once the user disconnects one, it stays off.
            #expect(store.profile(flagged)?.state == .disconnected)

            // The app's own store, as at launch.
            let launched = try ProfileStore(socketPath: daemon.socketPath, credentialStore: InMemoryCredentialStore())
            launched.start()
            do {
                try await waitUntil("the first snapshot") { launched.connection == .connected }
                try await Task.sleep(for: .milliseconds(500))
                #expect(launched.profile(flagged)?.state == .disconnected)
                #expect(launched.profile(flagged)?.desiredEnabled == false)
            } catch {
                await launched.stop()
                throw error
            }
            await launched.stop()
        }
    }
}

@MainActor
struct DaemonFailureTests {
    @Test(arguments: [
        (RPCError.Code.permissionDenied, DaemonFailure.permissionDenied),
        (.unavailable, .unavailable),
        (.notFound, .notFound),
        (.invalidArgument, .rejected(message: "reason")),
        (.failedPrecondition, .rejected(message: "reason")),
        (.internalError, .other(message: "reason")),
    ])
    func mapsStatusCodes(code: RPCError.Code, expected: DaemonFailure) {
        #expect(DaemonFailure(RPCError(code: code, message: "reason")) == expected)
    }

    @Test func wrapsForeignErrors() {
        struct Boom: Error {}
        guard case .other = DaemonFailure(Boom()) else {
            Issue.record("a foreign error was not wrapped")
            return
        }
    }
}

struct DaemonLocationTests {
    @Test func usesTheProductionSocketWithoutAnOverride() {
        #expect(DaemonLocation.socketPath(environment: [:]) == "/var/run/plaitway/plaitwayd.sock")
        #expect(DaemonLocation.socketPath(environment: ["PLAITWAY_SOCKET": ""]) == "/var/run/plaitway/plaitwayd.sock")
        #expect(DaemonLocation.override(environment: [:]) == nil)
    }

    #if DEBUG
    @Test func honoursPlaitwaySocketInADebugBuild() {
        let environment = ["PLAITWAY_SOCKET": "/tmp/x.sock", "TMPDIR": "/ignored/"]
        #expect(DaemonLocation.socketPath(environment: environment) == "/tmp/x.sock")
        #expect(DaemonLocation.override(environment: environment) == "/tmp/x.sock")
    }
    #else
    @Test func ignoresPlaitwaySocketInAReleaseBuild() {
        // The signed app hands saved credentials to whatever listens on the socket it connects to.
        let environment = ["PLAITWAY_SOCKET": "/tmp/x.sock"]
        #expect(DaemonLocation.socketPath(environment: environment) == DaemonLocation.productionSocketPath)
        #expect(DaemonLocation.override(environment: environment) == nil)
    }
    #endif
}
