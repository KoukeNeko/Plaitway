import Foundation
import Observation
import os
import ServiceManagement

/// Whether the privileged helper (the daemon's LaunchDaemon) is installed,
/// as SMAppService reports it.
public enum DaemonRegistration: Equatable, Sendable {
    /// Not running from an .app bundle (a development build): there is nothing
    /// to register, and a debug build talks to whatever daemon `PLAITWAY_SOCKET` names.
    case notBundled
    case notRegistered
    /// Not registered, and the app does not run from /Applications (a disk
    /// image, Downloads, a build directory). launchd starts the helper from
    /// inside the app at every boot, so it is only registered from there.
    case misplaced
    /// Registered, but an administrator has to allow it in System Settings.
    case requiresApproval
    case enabled
    /// The bundle has no launchd plist for the helper.
    case notFound
}

/// What `DaemonInstaller` needs from ServiceManagement, so that tests can stand
/// in for it.
@MainActor
public protocol DaemonService {
    var registration: DaemonRegistration { get }
    /// Whether the app runs from where the helper can be registered.
    var isInstallLocation: Bool { get }
    func register() throws
    func unregister() async throws
}

/// The helper as SMAppService knows it.
@MainActor
public struct SystemDaemonService: DaemonService {
    private let service = SMAppService.daemon(plistName: DaemonInstaller.plistName)

    public init() {}

    public var registration: DaemonRegistration {
        // Bundle.main is the directory of the executable when it is not an app,
        // and SMAppService would then report `notFound`, which means something else.
        guard Bundle.main.bundleURL.pathExtension == "app" else { return .notBundled }
        switch service.status {
        case .notRegistered: return notRegisteredHere
        case .enabled: return .enabled
        case .requiresApproval: return .requiresApproval
        case .notFound: return hasLaunchDaemonPlist ? notRegisteredHere : .notFound
        @unknown default: return .notFound
        }
    }

    public var isInstallLocation: Bool { Self.isInstallLocation(bundleURL: Bundle.main.bundleURL) }

    /// The helper's launchd job points into the app bundle, and launchd starts it
    /// at every boot, before anyone has opened the app: a disk image, Downloads
    /// or a translocated copy would break it, and a build directory is not meant to stay.
    public static func isInstallLocation(bundleURL: URL) -> Bool {
        bundleURL.path.hasPrefix("/Applications/")
    }

    private var notRegisteredHere: DaemonRegistration {
        isInstallLocation ? .notRegistered : .misplaced
    }

    /// SMAppService also answers `notFound` for a helper that was never
    /// registered when it has not seen the app yet (an app outside /Applications,
    /// say). The plist in the bundle is what registering needs, so with it the
    /// helper is simply not installed.
    private var hasLaunchDaemonPlist: Bool {
        let plist = Bundle.main.bundleURL
            .appendingPathComponent("Contents/Library/LaunchDaemons")
            .appendingPathComponent(DaemonInstaller.plistName)
        return FileManager.default.fileExists(atPath: plist.path)
    }

    public func register() throws { try service.register() }
    public func unregister() async throws { try await service.unregister() }
}

/// Installs, removes and reinstalls the helper. `registration` is what the UI
/// shows; call `refresh()` when the user may have changed it in System Settings.
@MainActor
@Observable
public final class DaemonInstaller {
    public static let plistName = "io.github.koukeneko.plaitway.daemon.plist"
    private static let logger = Logger(subsystem: "Plaitway", category: "DaemonInstaller")

    public enum Refusal: Error, Equatable {
        /// The app does not run from /Applications.
        case notInApplications
    }

    public private(set) var registration: DaemonRegistration
    /// When this process last registered the helper, so that the UI can tell a
    /// daemon that is still starting from one that does not answer.
    public private(set) var lastRegistered: Date?

    @ObservationIgnored private let service: any DaemonService

    public init(service: any DaemonService = SystemDaemonService()) {
        self.service = service
        registration = service.registration
    }

    public func refresh() {
        registration = service.registration
    }

    /// Registers the helper. A first registration ends in `.requiresApproval`
    /// until an administrator allows it in System Settings; that is not an error.
    public func install() throws {
        try requireInstallLocation()
        defer { refresh() }
        do {
            try service.register()
            lastRegistered = Date()
        } catch {
            // SMAppService reports "not allowed yet" by throwing as well.
            refresh()
            guard registration == .requiresApproval else { throw error }
            Self.logger.info("the helper waits for approval: \(error)")
        }
    }

    public func uninstall() async throws {
        defer { refresh() }
        try await service.unregister()
    }

    /// Replaces a helper that is older than the app: the registration makes
    /// launchd start the daemon from the app bundle again.
    public func reinstall() async throws {
        try requireInstallLocation()
        if registration != .notRegistered {
            try await uninstall()
        }
        try install()
    }

    private func requireInstallLocation() throws {
        guard service.isInstallLocation else { throw Refusal.notInApplications }
    }

    public func openSystemSettings() {
        SMAppService.openSystemSettingsLoginItems()
    }
}

/// The app's own entry in Login Items.
@MainActor
@Observable
public final class LoginItem {
    public private(set) var isEnabled: Bool
    public let isAvailable: Bool

    public init() {
        isAvailable = Bundle.main.bundleURL.pathExtension == "app"
        isEnabled = isAvailable && SMAppService.mainApp.status == .enabled
    }

    public func refresh() {
        isEnabled = isAvailable && SMAppService.mainApp.status == .enabled
    }

    public func setEnabled(_ enabled: Bool) throws {
        defer { refresh() }
        if enabled {
            try SMAppService.mainApp.register()
        } else {
            try SMAppService.mainApp.unregister()
        }
    }
}
