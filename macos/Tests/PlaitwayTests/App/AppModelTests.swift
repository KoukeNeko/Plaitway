import Foundation
import PlaitwayClient
import Testing
@testable import PlaitwayMenuBar

@MainActor
struct AppModelTests {
    private func makeModel(
        _ store: ProfileStore,
        registration: DaemonRegistration = .enabled,
        appVersion: String? = nil,
        isOverridden: Bool = false
    ) -> (AppModel, FakeDaemonService) {
        let service = FakeDaemonService(registration)
        let model = AppModel(
            store: store, installer: DaemonInstaller(service: service), loginItem: LoginItem(),
            appVersion: appVersion, isOverridden: isOverridden
        )
        return (model, service)
    }

    /// A directory with the given files, removed when the body returns.
    private func withFiles(_ files: [String: String], _ body: (URL) async throws -> Void) async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("pw-app-\(UUID().uuidString.prefix(8))")
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        for (name, content) in files {
            try Data(content.utf8).write(to: directory.appendingPathComponent(name))
        }
        try await body(directory)
    }

    private static let certificate = "-----BEGIN CERTIFICATE-----\nMIIBtest\n-----END CERTIFICATE-----\n"

    // MARK: import

    @Test func importsDroppedFilesInlinesTheirReferencesAndSelectsTheLastProfile() async throws {
        try await withDaemon { _, store in
            let (model, _) = makeModel(store)
            try await withFiles([
                "office.ovpn": "client\nremote vpn.example.net 1194\nca ca.crt\n",
                "ca.crt": Self.certificate,
                "home.conf": String(decoding: Fixture.wireGuard(), as: UTF8.self),
            ]) { directory in
                await model.importFiles([directory.appendingPathComponent("office.ovpn"), directory.appendingPathComponent("home.conf")])
            }

            #expect(store.profiles.map(\.name) == ["office", "home"])
            #expect(store.profiles.map(\.kind) == [.openvpn, .wireguard])
            #expect(model.importReport == nil, "a clean import needs no sheet")
            let last = try #require(store.profiles.last)
            #expect(model.selection == .profile(last.id))
        }
    }

    @Test func importsAProfileWhateverItsFileIsCalled() async throws {
        try await withDaemon { _, store in
            let (model, _) = makeModel(store)
            try await withFiles([
                "work.vpn": "client\nremote vpn.example.net 1194\n",
                "wgs_client-4": String(decoding: Fixture.wireGuard(), as: UTF8.self),
                "home.WG": String(decoding: Fixture.wireGuard(), as: UTF8.self),
            ]) { directory in
                await model.importFiles(["work.vpn", "wgs_client-4", "home.WG"].map { directory.appendingPathComponent($0) })
            }
            #expect(store.profiles.map(\.kind) == [.openvpn, .wireguard, .wireguard])
            #expect(model.importReport == nil)
        }
    }

    @Test func reportsWhatWentWrongPerFile() async throws {
        try await withDaemon { _, store in
            let (model, _) = makeModel(store)
            try await withFiles([
                "fine.ovpn": "client\nremote vpn.example.net 1194\n",
                "scripted.ovpn": "client\nremote vpn.example.net 1194\nup /bin/sh\n",
                "bad.ovpn": "client\n# fake: reject\n",
                "missing.ovpn": "client\nca nowhere.crt\n",
                "notes.txt": "client\n# fake: reject\n",
            ]) { directory in
                let names = ["fine.ovpn", "scripted.ovpn", "bad.ovpn", "missing.ovpn", "notes.txt", "gone.ovpn"]
                await model.importFiles(names.map { directory.appendingPathComponent($0) })
            }

            let report = try #require(model.importReport)
            #expect(report.needsAttention)
            #expect(report.outcomes.map(\.filename) == ["fine.ovpn", "scripted.ovpn", "bad.ovpn", "missing.ovpn", "notes.txt", "gone.ovpn"])

            guard case .imported(_, let fineWarnings) = report.outcomes[0].result else { Issue.record("fine.ovpn was not imported"); return }
            #expect(fineWarnings.isEmpty)
            guard case .imported(_, let warnings) = report.outcomes[1].result else { Issue.record("scripted.ovpn was not imported"); return }
            #expect(warnings.map(\.directive) == ["up"])
            guard case .failed(let rejection) = report.outcomes[2].result else { Issue.record("bad.ovpn was not refused"); return }
            #expect(rejection.contains("rejected"), "the daemon's reason is shown: \(rejection)")
            guard case .failed(let missing) = report.outcomes[3].result else { Issue.record("missing.ovpn was not refused"); return }
            #expect(missing.contains("nowhere.crt"))
            // The extension decides nothing: the daemon judged the content of notes.txt.
            guard case .failed(let refused) = report.outcomes[4].result else { Issue.record("notes.txt was not refused"); return }
            #expect(refused.contains("rejected"), "the daemon's reason is shown: \(refused)")
            guard case .failed = report.outcomes[5].result else { Issue.record("a missing file was not refused"); return }

            // Only the two that the daemon accepted exist.
            #expect(store.profiles.map(\.name) == ["fine", "scripted"])
        }
    }

    @Test func aFailedImportSelectsNothing() async throws {
        try await withDaemon { _, store in
            let (model, _) = makeModel(store)
            try await withFiles(["bad.ovpn": "client\n# fake: reject\n"]) { directory in
                await model.importFiles([directory.appendingPathComponent("bad.ovpn")])
            }
            #expect(model.selection == nil)
            #expect(store.profiles.isEmpty)
        }
    }

    @Test func anAuthUserPassFileBecomesSavedCredentialsAndTheFirstConnectionDoesNotAsk() async throws {
        let saved = InMemoryCredentialStore()
        try await withDaemon(credentials: saved) { _, store in
            let (model, _) = makeModel(store)
            try await withFiles([
                "router.ovpn": "client\nremote vpn.example.net 1194\n# fake: needs-credentials\nauth-user-pass creds.txt\n",
                "creds.txt": "alice\ns3cret\n",
            ]) { directory in
                await model.importFiles([directory.appendingPathComponent("router.ovpn")])
            }

            #expect(model.importReport == nil, "a clean import needs no sheet")
            let id = try #require(store.profiles.first?.id)
            #expect(try await saved.credentials(profileID: id, kind: .userPassword) == Credentials(username: "alice", password: "s3cret"))

            try await store.setEnabled(true, profileID: id)
            try await waitUntil("the connection") {
                #expect(store.credentialPrompts.isEmpty, "the sheet was shown although the credentials came with the profile")
                return store.profile(id)?.state == .connected
            }
        }
    }

    @Test func aProfileThatNamesAFileOutsideItsFolderIsRefusedAndNothingIsStored() async throws {
        let saved = InMemoryCredentialStore()
        try await withDaemon(credentials: saved) { _, store in
            let (model, _) = makeModel(store)
            try await withFiles(["router.ovpn": "client\nremote vpn.example.net 1194\nauth-user-pass /etc/hosts\n"]) { directory in
                await model.importFiles([directory.appendingPathComponent("router.ovpn")])
            }

            let report = try #require(model.importReport)
            guard case .failed(let message) = report.outcomes[0].result else { Issue.record("the profile was not refused"); return }
            #expect(message.contains("/etc/hosts"), "the refused file is named: \(message)")
            #expect(store.profiles.isEmpty)
        }
    }

    @Test func theDaemonsReasonForRefusingAPkcs12ProfileIsShown() async throws {
        // The fake backend takes any profile; the real parser is what refuses a file reference.
        try await withDaemon(fake: false) { _, store in
            let (model, _) = makeModel(store)
            try await withFiles(["router.ovpn": "client\nremote vpn.example.net 1194\npkcs12 client.p12\n"]) { directory in
                await model.importFiles([directory.appendingPathComponent("router.ovpn")])
            }

            let report = try #require(model.importReport)
            #expect(report.needsAttention)
            guard case .failed(let message) = report.outcomes[0].result else { Issue.record("the profile was not refused"); return }
            #expect(message.contains("pkcs12"), "the daemon's reason is shown: \(message)")
            #expect(store.profiles.isEmpty)
        }
    }

    // MARK: delete, reorder, edit

    @Test func deletionAsksFirstAndThenSelectsAnotherProfile() async throws {
        try await withDaemon { _, store in
            let (model, _) = makeModel(store)
            let a = try await store.importFixture("a.ovpn")
            let b = try await store.importFixture("b.ovpn")
            model.selection = .profile(b)

            model.requestDeletion(of: b)
            #expect(model.pendingDeletion == b)
            try await Task.sleep(for: .milliseconds(200))
            #expect(store.profiles.count == 2, "nothing is deleted before the confirmation")

            await model.delete(profileID: b)
            try await waitUntil("the profile to go") { store.profile(b) == nil }
            model.reconcileSelection()
            #expect(model.selection == .profile(a))

            // The last one: nothing is left to select.
            model.requestDeletion(of: a)
            await model.delete(profileID: a)
            try await waitUntil("the last profile to go") { store.profiles.isEmpty }
            model.reconcileSelection()
            #expect(model.selection == nil)
        }
    }

    @Test func reconcilingKeepsASelectionThatStillExists() async throws {
        try await withDaemon { _, store in
            let (model, _) = makeModel(store)
            let a = try await store.importFixture("a.ovpn")
            _ = try await store.importFixture("b.ovpn")
            model.selection = .profile(a)
            model.reconcileSelection()
            #expect(model.selection == .profile(a))
            model.selection = .diagnostics
            model.reconcileSelection()
            #expect(model.selection == .diagnostics)
            model.selection = nil
            model.reconcileSelection()
            #expect(model.selection == .profile(a), "something is selected as soon as there is something")
        }
    }

    @Test func draggingAProfileSetsThePriorityOrder() async throws {
        try await withDaemon { _, store in
            let (model, _) = makeModel(store)
            let a = try await store.importFixture("a.ovpn")
            let b = try await store.importFixture("b.ovpn")
            let c = try await store.importFixture("c.ovpn")

            await model.move(fromOffsets: [2], toOffset: 0)
            // The daemon reports the new priorities one profile at a time; the store settles on the last.
            try await waitUntil("c first") { store.profiles.map(\.id) == [c, a, b] && store.profiles.map(\.settings.priority) == [1, 2, 3] }

            await model.move(profileID: c, by: 1)
            try await waitUntil("c second") { store.profiles.map(\.id) == [a, c, b] }

            // Past the end nothing happens, and nothing is reported.
            await model.move(profileID: b, by: 1)
            await model.move(profileID: a, by: -1)
            #expect(model.alert == nil)
            #expect(store.profiles.map(\.id) == [a, c, b])
        }
    }

    @Test func renamingTrimsAndIgnoresNothingNew() async throws {
        try await withDaemon { _, store in
            let (model, _) = makeModel(store)
            let id = try await store.importFixture("office.ovpn")

            await model.rename(profileID: id, to: "  Head Office \n")
            try await waitUntil("the rename") { store.profile(id)?.name == "Head Office" }

            await model.rename(profileID: id, to: "   ")
            await model.rename(profileID: id, to: "Head Office")
            try await Task.sleep(for: .milliseconds(150))
            #expect(store.profile(id)?.name == "Head Office")
            #expect(model.alert == nil)
        }
    }

    @Test func changingOneSettingKeepsTheOthers() async throws {
        try await withDaemon { _, store in
            let (model, _) = makeModel(store)
            let id = try await store.importFixture("office.ovpn")

            await model.changeSettings(profileID: id) { $0.autoConnect = true }
            try await waitUntil("auto-connect") { store.profile(id)?.settings.autoConnect == true }
            await model.changeSettings(profileID: id) { $0.tunnelMode = .split }
            try await waitUntil("split tunnel") { store.profile(id)?.settings.tunnelMode == .split }
            #expect(store.profile(id)?.settings.autoConnect == true)
            #expect(store.profile(id)?.settings.priority == 1)
        }
    }

    // MARK: failures

    @Test func aCallThatFailsBecomesAnAlert() async throws {
        try await withDaemon { daemon, store in
            let (model, _) = makeModel(store)
            let id = try await store.importFixture("office.ovpn")
            #expect(model.alert == nil)

            daemon.kill()
            try await waitUntil("the outage") { store.connection == .unavailable }
            await model.rename(profileID: id, to: "Other")

            let alert = try #require(model.alert)
            #expect(alert.title == AppAlert.Title.saveFailed.text)
            #expect(alert.message == userMessage(for: DaemonFailure.unavailable))
        }
    }

    @Test func daemonFailuresAreWorded() {
        #expect(userMessage(for: DaemonFailure.permissionDenied) == String(localized: "Administrator required", bundle: .module))
        #expect(userMessage(for: DaemonFailure.rejected(message: "line 3: nope")) == "line 3: nope")
        #expect(userMessage(for: ProfileImporter.Failure.missingFile(directive: "ca", path: "/x/ca.crt")).contains("/x/ca.crt"))
        // The file that was refused is named, so the user knows which line of the profile to look at.
        let outside = userMessage(for: ProfileImporter.Failure.outsideProfileDirectory(directive: "key", path: "../id_ed25519"))
        #expect(outside.contains("../id_ed25519") && outside.contains("key"))
        #expect(userMessage(for: ProfileImporter.Failure.notCredentials(path: "/x/creds.txt")).contains("/x/creds.txt"))
    }

    // MARK: helper

    @Test func tracksTheHelperThroughItsStates() async throws {
        try await withDaemon { daemon, store in
            let (model, service) = makeModel(store, registration: .enabled, appVersion: "0.1.0")
            #expect(model.setup == .versionMismatch(daemon: store.daemonInfo?.version ?? "", app: "0.1.0") || store.daemonInfo == nil)
            try await waitUntil("the daemon's version") { store.daemonInfo != nil }
            #expect(model.setup == .versionMismatch(daemon: "0.0.0-dev", app: "0.1.0"))

            daemon.kill()
            try await waitUntil("the outage") { store.connection == .unavailable }
            #expect(model.setup == .notResponding)

            // A retry waits for the daemon for a while before giving up on it.
            model.retry()
            #expect(model.setup == .connecting)

            service.registration = .notRegistered
            model.installer.refresh()
            #expect(model.setup == .needsInstall)

            service.afterRegister = .requiresApproval
            model.installHelper()
            #expect(model.setup == .needsApproval)
            #expect(model.alert == nil)
        }
    }

    @Test func aFailedInstallIsReported() async throws {
        try await withDaemon { daemon, store in
            let (model, service) = makeModel(store, registration: .notRegistered)
            service.afterRegister = .notRegistered
            service.registerFails = true
            model.installHelper()
            #expect(model.alert?.title == AppAlert.Title.helperFailed.text)
            #expect(model.installer.registration == .notRegistered)
        }
    }

    @Test func uninstallingGoesThroughTheInstaller() async throws {
        try await withDaemon { _, store in
            let (model, service) = makeModel(store, registration: .enabled)
            await model.uninstallHelper()
            #expect(service.calls == ["unregister"])
            #expect(model.installer.registration == .notRegistered)
        }
    }

    @Test func aFailedInstallPointsToTheScriptThatInstallsWithoutApproval() async throws {
        try await withDaemon { _, store in
            let (model, service) = makeModel(store, registration: .notRegistered)
            service.afterRegister = .notRegistered
            service.registerFails = true
            model.installHelper()
            #expect(model.alert?.message.contains("scripts/dev-install-daemon.sh") == true)

            // Refused for where the app runs: the reason is worded, and nothing was registered.
            service.registerFails = false
            service.isInstallLocation = false
            service.calls.removeAll()
            model.installHelper()
            #expect(model.alert?.message.contains(userMessage(for: DaemonInstaller.Refusal.notInApplications)) == true)
            #expect(service.calls.isEmpty)
        }
    }

    // MARK: a helper that something else installed

    @Test(arguments: [DaemonRegistration.notRegistered, .misplaced])
    func aDaemonThatAnswersWithoutHavingBeenRegisteredByTheAppIsLeftAlone(registration: DaemonRegistration) async throws {
        try await withDaemon { _, store in
            let (model, service) = makeModel(store, registration: registration, appVersion: "9.9.9")
            try await waitUntil("the daemon's version") { store.daemonInfo != nil }
            #expect(model.isHelperExternal)
            #expect(model.setup == .versionMismatch(daemon: "0.0.0-dev", app: "9.9.9"))
            #expect(!model.setup.offersInstallation, "the window would open at every launch")

            // Registering again would add a second job with the label of the one that runs.
            await model.reinstallHelper()
            #expect(service.calls.isEmpty, "the helper was registered a second time: \(service.calls)")
            #expect(model.alert == nil)
        }
    }

    @Test func aHelperTheAppRegisteredIsNotExternalAndIsReinstalledAfterTheConfirmation() async throws {
        try await withDaemon { _, store in
            let (model, service) = makeModel(store, registration: .enabled)
            #expect(!model.isHelperExternal)

            // Reinstalling drops every tunnel, so every way to it asks first.
            await model.perform(.reinstallHelper)
            #expect(model.isConfirmingReinstall)
            #expect(service.calls.isEmpty, "the helper was reinstalled before the confirmation")

            await model.reinstallHelper()
            #expect(service.calls == ["unregister", "register"])
        }
    }

    @Test func aDaemonOfTheDeveloperOrOneThatIsDownIsNotAnExternalHelper() async throws {
        try await withDaemon { daemon, store in
            let (overridden, _) = makeModel(store, registration: .notRegistered, isOverridden: true)
            #expect(!overridden.isHelperExternal)

            let (model, _) = makeModel(store, registration: .notRegistered)
            #expect(model.isHelperExternal)
            daemon.kill()
            try await waitUntil("the outage") { store.connection == .unavailable }
            #expect(!model.isHelperExternal)
            #expect(model.setup == .needsInstall)
        }
    }

    // MARK: quitting

    @Test func profilesThatAreOnAreWhatQuittingLeavesRunning() async throws {
        try await withDaemon { daemon, store in
            let (model, _) = makeModel(store)
            let office = try await store.importFixture("office.ovpn")
            let lab = try await store.importFixture("lab.ovpn")
            _ = try await store.importFixture("idle.ovpn")
            #expect(model.switchedOnProfiles.isEmpty, "nothing is on, so Quit has nothing to ask")

            try await store.setEnabled(true, profileID: office)
            try await store.setEnabled(true, profileID: lab)
            try await waitUntil("both connected") { [office, lab].allSatisfy { store.profile($0)?.state == .connected } }
            #expect(Set(model.switchedOnProfiles.map(\.id)) == [office, lab])

            #expect(await model.disconnectAll())
            try await waitUntil("both disconnected") { [office, lab].allSatisfy { store.profile($0)?.state == .disconnected } }
            #expect(model.switchedOnProfiles.isEmpty)
            #expect(model.alert == nil)

            // A helper that does not answer says nothing true about its profiles.
            try await store.setEnabled(true, profileID: office)
            try await waitUntil("connected again") { store.profile(office)?.state == .connected }
            daemon.kill()
            try await waitUntil("the outage") { store.connection == .unavailable }
            #expect(model.switchedOnProfiles.isEmpty)
        }
    }

    // MARK: renaming

    @Test func aRefusedRenameSaysSoSoTheFieldCanGoBack() async throws {
        try await withDaemon { daemon, store in
            let (model, _) = makeModel(store)
            let id = try await store.importFixture("office.ovpn")

            #expect(await model.rename(profileID: id, to: "Head Office"))
            try await waitUntil("the rename") { store.profile(id)?.name == "Head Office" }
            #expect(await model.rename(profileID: id, to: " Head Office "), "the name it has already is not a refusal")
            #expect(await model.rename(profileID: id, to: "   ") == false, "an empty name is not applied")

            daemon.kill()
            try await waitUntil("the outage") { store.connection == .unavailable }
            #expect(await model.rename(profileID: id, to: "Other") == false)
            #expect(model.alert?.title == AppAlert.Title.saveFailed.text)
        }
    }

    // MARK: logs and diagnostics

    @Test func followsAProfilesLogAndStopsWhenCancelled() async throws {
        try await withDaemon { _, store in
            let id = try await store.importFixture("office.ovpn")
            try await store.setEnabled(true, profileID: id)
            let tail = LogTail()
            let task = Task { await tail.run(store: store, profileID: id) }

            try await waitUntil("the connection to be logged") { tail.entries.contains { $0.text.hasPrefix("connected on") } }
            #expect(tail.plainText.contains("connected on"))
            task.cancel()
            await task.value
        }
    }

    @Test func aLogOfADeletedProfileEnds() async throws {
        try await withDaemon { _, store in
            let tail = LogTail()
            // notFound ends the loop instead of retrying forever.
            await tail.run(store: store, profileID: "nope")
            #expect(tail.entries.isEmpty)
        }
    }

    @Test func keepsTheNewestLogLinesWithinItsCapacity() {
        let tail = LogTail()
        for number in 0..<(LogTail.capacity + 500) {
            var line = LogLine()
            line.text = "line \(number)"
            tail.append(line)
        }
        #expect(tail.entries.count <= LogTail.capacity + 200)
        #expect(tail.entries.count >= LogTail.capacity)
        #expect(tail.entries.last?.text == "line \(LogTail.capacity + 499)")
        #expect(Set(tail.entries.map(\.id)).count == tail.entries.count, "line ids stay unique")
    }

    @Test func showsLinesThatComeTogetherInOneUpdate() async throws {
        let tail = LogTail()
        for number in 0..<100 {
            var line = LogLine()
            line.text = "line \(number)"
            tail.enqueue(line)
        }
        #expect(tail.entries.isEmpty, "the page is not drawn again for each line")
        try await waitUntil("the lines to be shown") { tail.entries.count == 100 }
        #expect(tail.entries.first?.text == "line 0")
    }

    @Test func readsDiagnosticsAndRefreshesAfterACommand() async throws {
        try await withDaemon { _, store in
            let (model, _) = makeModel(store)
            let diagnostics = DiagnosticsModel()
            let task = Task { await diagnostics.run(store: store) }
            try await waitUntil("the first reading") { diagnostics.diagnostics != nil }
            #expect(diagnostics.diagnostics?.staleRoutes.map(\.key) == ["fake-stale-1"])

            await model.removeStaleRoute(key: "fake-stale-1")
            await diagnostics.refresh(store: store)
            #expect(diagnostics.diagnostics?.staleRoutes.isEmpty == true)
            #expect(model.alert == nil)

            // Removing it again finds it gone already, which is what was asked for.
            await model.removeStaleRoute(key: "fake-stale-1")
            #expect(model.alert == nil, "an alert for a route that is already gone: \(model.alert?.message ?? "")")
            await model.resync()
            await diagnostics.refresh(store: store)
            #expect(diagnostics.diagnostics?.network.lastChangeReason == "manual")
            task.cancel()
            await task.value
        }
    }
}
