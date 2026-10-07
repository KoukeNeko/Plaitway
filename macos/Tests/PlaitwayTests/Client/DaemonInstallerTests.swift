import Foundation
import PlaitwayClient
import Testing

@MainActor
final class FakeDaemonService: DaemonService {
    struct Refusal: Error {}

    var registration: DaemonRegistration
    var isInstallLocation = true
    /// What registering leads to.
    var afterRegister: DaemonRegistration = .enabled
    var registerFails = false
    var calls: [String] = []

    init(_ registration: DaemonRegistration) {
        self.registration = registration
    }

    func register() throws {
        calls.append("register")
        registration = afterRegister
        if registerFails { throw Refusal() }
    }

    func unregister() async throws {
        calls.append("unregister")
        registration = .notRegistered
    }
}

@MainActor
struct DaemonInstallerTests {
    @Test func installsTheHelper() throws {
        let service = FakeDaemonService(.notRegistered)
        let installer = DaemonInstaller(service: service)
        #expect(installer.registration == .notRegistered)

        try installer.install()
        #expect(installer.registration == .enabled)
        #expect(installer.lastRegistered != nil)
        #expect(service.calls == ["register"])
    }

    @Test func waitingForApprovalIsNotAnError() throws {
        let service = FakeDaemonService(.notRegistered)
        service.afterRegister = .requiresApproval
        service.registerFails = true // SMAppService throws for a registration that needs approval
        let installer = DaemonInstaller(service: service)

        try installer.install()
        #expect(installer.registration == .requiresApproval)
    }

    @Test func otherRegistrationFailuresAreReported() {
        let service = FakeDaemonService(.notRegistered)
        service.afterRegister = .notRegistered
        service.registerFails = true
        let installer = DaemonInstaller(service: service)

        #expect(throws: FakeDaemonService.Refusal.self) { try installer.install() }
        #expect(installer.registration == .notRegistered)
    }

    @Test func refreshPicksUpTheUsersApproval() {
        let service = FakeDaemonService(.requiresApproval)
        let installer = DaemonInstaller(service: service)
        #expect(installer.registration == .requiresApproval)

        service.registration = .enabled
        installer.refresh()
        #expect(installer.registration == .enabled)
    }

    @Test func uninstallsTheHelper() async throws {
        let service = FakeDaemonService(.enabled)
        let installer = DaemonInstaller(service: service)

        try await installer.uninstall()
        #expect(installer.registration == .notRegistered)
        #expect(service.calls == ["unregister"])
    }

    @Test func reinstallingReplacesARegisteredHelper() async throws {
        let service = FakeDaemonService(.enabled)
        let installer = DaemonInstaller(service: service)

        try await installer.reinstall()
        #expect(service.calls == ["unregister", "register"])
        #expect(installer.registration == .enabled)
    }

    @Test func reinstallingAnUnregisteredHelperOnlyRegisters() async throws {
        // Unregistering a service that is not registered fails with EPERM.
        let service = FakeDaemonService(.notRegistered)
        let installer = DaemonInstaller(service: service)

        try await installer.reinstall()
        #expect(service.calls == ["register"])
    }

    @Test func refusesToRegisterFromOutsideTheApplicationsFolder() async throws {
        let service = FakeDaemonService(.misplaced)
        service.isInstallLocation = false
        let installer = DaemonInstaller(service: service)

        #expect(throws: DaemonInstaller.Refusal.notInApplications) { try installer.install() }
        await #expect(throws: DaemonInstaller.Refusal.notInApplications) { try await installer.reinstall() }
        #expect(service.calls.isEmpty, "the helper was registered from the wrong place: \(service.calls)")

        // Also when the system reports a registration that came from somewhere else.
        service.registration = .enabled
        installer.refresh()
        await #expect(throws: DaemonInstaller.Refusal.notInApplications) { try await installer.reinstall() }
        #expect(service.calls.isEmpty)
    }

    @Test(arguments: [
        ("/Applications/Plaitway.app", true),
        ("/Applications/Utilities/Plaitway.app", true),
        ("/Volumes/Plaitway/Plaitway.app", false),
        ("/Users/someone/Downloads/Plaitway.app", false),
        ("/Users/someone/Applications/Plaitway.app", false),
        ("/private/var/folders/ab/cdef/T/AppTranslocation/0E8A/d/Plaitway.app", false),
        ("/Users/someone/code/Plaitway/build/Plaitway.app", false),
        ("/Applications.app", false),
    ])
    func theHelperIsRegisteredOnlyFromApplications(path: String, expected: Bool) {
        #expect(SystemDaemonService.isInstallLocation(bundleURL: URL(fileURLWithPath: path)) == expected, "\(path)")
    }

    @Test func runningOutsideABundleIsNotAnInstallState() {
        // The test runner is not an app bundle either.
        #expect(DaemonInstaller().registration == .notBundled)
        #expect(LoginItem().isAvailable == false)
    }
}
