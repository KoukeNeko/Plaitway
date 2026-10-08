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
    /// Registrations that fail before one works, as the first one does just after an unregister.
    var registerFailuresBeforeSuccess = 0
    var calls: [String] = []

    init(_ registration: DaemonRegistration) {
        self.registration = registration
    }

    func register() throws {
        calls.append("register")
        if registerFailuresBeforeSuccess > 0 {
            registerFailuresBeforeSuccess -= 1
            registration = .notRegistered
            throw Refusal()
        }
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

    // macOS answers "invalid record generation" (shown as "Operation not permitted") to a
    // registration made right after an unregistration; the next one is accepted.
    @Test func reinstallingTriesAgainWhenTheRegistrationRightAfterTheUnregistrationFails() async throws {
        let service = FakeDaemonService(.enabled)
        service.registerFailuresBeforeSuccess = 2
        let installer = DaemonInstaller(service: service, registerRetryDelay: .milliseconds(1))

        try await installer.reinstall()
        #expect(service.calls == ["unregister", "register", "register", "register"])
        #expect(installer.registration == .enabled)
    }

    @Test func reinstallingReportsAFailureThatLasts() async {
        let service = FakeDaemonService(.enabled)
        service.registerFailuresBeforeSuccess = 100
        let installer = DaemonInstaller(service: service, registerRetryDelay: .milliseconds(1))

        await #expect(throws: FakeDaemonService.Refusal.self) { try await installer.reinstall() }
        #expect(service.calls.filter { $0 == "register" }.count == 5, "five tries, not forever")
        #expect(installer.registration == .notRegistered)
    }

    @Test func aFirstRegistrationIsNotTriedAgain() {
        let service = FakeDaemonService(.notRegistered)
        service.registerFailuresBeforeSuccess = 1
        let installer = DaemonInstaller(service: service, registerRetryDelay: .milliseconds(1))

        #expect(throws: FakeDaemonService.Refusal.self) { try installer.install() }
        #expect(service.calls == ["register"])
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
