import Foundation
import GRPCCore
import PlaitwayClient
import Testing

@MainActor
struct ProfileManagementTests {
    // MARK: import

    @Test func importsAnOpenVPNProfile() async throws {
        try await withDaemon { _, store in
            let result = try await store.importProfile(
                content: Fixture.openVPN(host: "vpn.example.net"), sourceFilename: "/Users/someone/Router.ovpn"
            )
            #expect(result.profile.kind == .openvpn)
            #expect(result.profile.name == "Router")
            #expect(result.profile.state == .disconnected)
            #expect(result.profile.summary.endpoints.first?.host == "vpn.example.net")
            #expect(result.profile.summary.endpoints.first?.protocol == "tcp")
            #expect(result.profile.summary.routes == ["192.168.1.0/24"])
            #expect(result.warnings.isEmpty)

            try await waitUntil("the profile to appear") { store.profile(result.profile.id) != nil }
        }
    }

    @Test func importsAWireGuardProfileAndKnowsItIsAFullTunnel() async throws {
        try await withDaemon { _, store in
            let result = try await store.importProfile(
                content: Fixture.wireGuard(), sourceFilename: "home.conf", name: "Home router"
            )
            #expect(result.profile.kind == .wireguard)
            #expect(result.profile.name == "Home router")
            #expect(result.profile.summary.redirectsDefaultRoute)
            #expect(result.profile.summary.addresses == ["10.6.0.2/32"])
        }
    }

    @Test func returnsWhatTheDaemonRemoved() async throws {
        try await withDaemon { _, store in
            let result = try await store.importProfile(
                content: Fixture.openVPN(extra: ["up /bin/sh"]), sourceFilename: "scripted.ovpn"
            )
            #expect(result.warnings.map(\.directive) == ["up"])
            #expect(result.warnings.first?.line == 6)
            #expect(result.warnings.first?.message.isEmpty == false)
        }
    }

    @Test func showsTheReasonARejectedProfileGives() async throws {
        try await withDaemon { _, store in
            do {
                _ = try await store.importProfile(
                    content: Fixture.openVPN(markers: ["# fake: reject"]), sourceFilename: "bad.ovpn"
                )
                Issue.record("a rejected profile was accepted")
            } catch let error as RPCError {
                #expect(error.code == .invalidArgument)
                #expect(error.message.contains("rejected"))
                guard case .rejected(let message) = DaemonFailure(error) else {
                    Issue.record("the rejection was not reported as such")
                    return
                }
                #expect(message == error.message)
            }
            #expect(store.profiles.isEmpty)
        }
    }

    @Test func refusesEmptyContent() async throws {
        try await withDaemon { _, store in
            await #expect(throws: RPCError.self) {
                _ = try await store.importProfile(content: Data(), sourceFilename: "empty.ovpn")
            }
        }
    }

    // MARK: update

    @Test func updatesNameAndSettings() async throws {
        try await withDaemon { _, store in
            let id = try await store.importFixture("office.ovpn")
            let first = try #require(store.profile(id)?.settings)

            var settings = first
            settings.autoConnect = true
            settings.tunnelMode = .split
            try await store.updateProfile(id: id, name: "Office", settings: settings)

            try await waitUntil("the update") { store.profile(id)?.name == "Office" }
            #expect(store.profile(id)?.settings.autoConnect == true)
            #expect(store.profile(id)?.settings.tunnelMode == .split)
            #expect(store.profile(id)?.settings.priority == first.priority)
        }
    }

    @Test func updatingOnlyTheNameKeepsTheSettings() async throws {
        try await withDaemon { _, store in
            var settings = ProfileSettings()
            settings.autoConnect = true
            settings.tunnelMode = .full
            let id = try await store.importFixture("office.ovpn", settings: settings)

            try await store.updateProfile(id: id, name: "Renamed")
            try await waitUntil("the rename") { store.profile(id)?.name == "Renamed" }
            #expect(store.profile(id)?.settings.autoConnect == true)
            #expect(store.profile(id)?.settings.tunnelMode == .full)
        }
    }

    @Test func updatingAnUnknownProfileIsNotFound() async throws {
        try await withDaemon { _, store in
            do {
                try await store.updateProfile(id: "nope", name: "x")
                Issue.record("an unknown profile id was accepted")
            } catch let error as RPCError {
                #expect(error.code == .notFound)
            }
        }
    }

    // MARK: reorder

    @Test func reordersByPriority() async throws {
        try await withDaemon { _, store in
            let a = try await store.importFixture("a.ovpn")
            let b = try await store.importFixture("b.ovpn")
            let c = try await store.importFixture("c.conf", Fixture.wireGuard())
            try await waitUntil("three profiles") { store.profiles.count == 3 }
            #expect(store.profiles.map(\.id) == [a, b, c])

            try await store.reorder(ids: [c, a, b])
            // The daemon reports the new priorities one profile at a time; the store settles on the last.
            try await waitUntil("the new order") { store.profiles.map(\.id) == [c, a, b] && store.profiles.map(\.settings.priority) == [1, 2, 3] }

            try await store.reorder(ids: [b, c, a])
            try await waitUntil("the second order") { store.profiles.map(\.id) == [b, c, a] }
        }
    }

    @Test func aReorderThatNamesAProfileTwiceOrLeavesOneOutIsRefused() async throws {
        try await withDaemon { _, store in
            let a = try await store.importFixture("a.ovpn")
            let b = try await store.importFixture("b.ovpn")
            try await waitUntil("two profiles") { store.profiles.count == 2 }

            await #expect(throws: RPCError.self) { try await store.reorder(ids: [a]) }
            await #expect(throws: RPCError.self) { try await store.reorder(ids: [a, a]) }
            #expect(store.profiles.map(\.id) == [a, b])
        }
    }

    // MARK: delete

    @Test func deletesAProfileAndDisconnectsItFirst() async throws {
        try await withDaemon { _, store in
            let keep = try await store.importFixture("keep.ovpn")
            let doomed = try await store.importFixture("doomed.ovpn")
            try await store.setEnabled(true, profileID: doomed)
            try await store.waitForState(doomed, .connected)

            try await store.deleteProfile(id: doomed)
            try await waitUntil("the profile to go") { store.profile(doomed) == nil }
            #expect(store.profiles.map(\.id) == [keep])
        }
    }

    @Test func deletingAnUnknownProfileIsNotFound() async throws {
        try await withDaemon { _, store in
            do {
                try await store.deleteProfile(id: "nope")
                Issue.record("an unknown profile id was accepted")
            } catch let error as RPCError {
                #expect(error.code == .notFound)
            }
        }
    }

    @Test func profilesSurviveARestartOfTheDaemonInTheirOrder() async throws {
        try await withDaemon { daemon, store in
            let a = try await store.importFixture("a.ovpn")
            let b = try await store.importFixture("b.ovpn")
            try await store.reorder(ids: [b, a])
            try await waitUntil("the order") { store.profiles.map(\.id) == [b, a] }

            daemon.kill()
            try await waitUntil("the outage") { store.connection == .unavailable }
            try await daemon.start()
            try await waitUntil("the reconnect") { store.connection == .connected }
            #expect(store.profiles.map(\.id) == [b, a])
        }
    }
}
