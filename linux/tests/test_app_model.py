import os
import shutil
import tempfile
import unittest

from plaitway.client.errors import DaemonFailure, FailureKind
from plaitway.client.types import CredentialKind, Credentials, ProfileState
from plaitway.core.app_model import (
    DIAGNOSTICS,
    SETTINGS,
    DiagnosticsPage,
    ProfileSection,
    Selection,
)
from plaitway.core.credentials import InMemoryCredentialStore
from plaitway.core.diagnostics_model import DiagnosticsModel
from plaitway.core.ports import HelperRegistration, QuitAnswer
from plaitway.core.profile_store import Connection
from plaitway.core.setup import SetupAction, SetupState
from plaitway.core.user_message import user_message

from support import NEEDS_CREDENTIALS, AppTestCase, DaemonTestCase, wireguard_profile

CERTIFICATE = "-----BEGIN CERTIFICATE-----\nMIIBtest\n-----END CERTIFICATE-----\n"


class FilesTestCase(AppTestCase):
    def files(self, files: dict[str, str]) -> str:
        """A directory with the given files, removed when the test is over."""
        directory = tempfile.mkdtemp(prefix="pw-app-")
        self.addCleanup(shutil.rmtree, directory, True)
        for name, content in files.items():
            with open(os.path.join(directory, name), "wb") as file:
                file.write(content.encode())
        return directory


class ImportTests(FilesTestCase):
    def test_imports_dropped_files_inlines_their_references_and_selects_the_last_profile(self):
        app = self.make_app()
        directory = self.files({
            "office.ovpn": "client\nremote vpn.example.net 1194\nca ca.crt\n",
            "ca.crt": CERTIFICATE,
            "home.conf": wireguard_profile().decode(),
        })
        app.model.import_files([os.path.join(directory, "office.ovpn"), os.path.join(directory, "home.conf")])
        app.wait_until(lambda: len(app.store.profiles) == 2 and app.model.selection is not None, "both profiles")

        self.assertEqual([p.name for p in app.store.profiles], ["office", "home"])
        self.assertEqual([p.kind for p in app.store.profiles], [1, 2])
        self.assertEqual(app.dialogs.import_reports, [], "a clean import needs no dialog")
        last = app.store.profiles[-1]
        app.wait_until(lambda: app.model.selection == Selection.profile(last.id), "the last profile to be selected")
        self.assertIn("<ca>", app.store.profile_content(app.store.profiles[0].id))

    def test_imports_a_profile_whatever_its_file_is_called(self):
        app = self.make_app()
        directory = self.files({
            "work.vpn": "client\nremote vpn.example.net 1194\n",
            "wgs_client-4": wireguard_profile().decode(),
            "home.WG": wireguard_profile().decode(),
        })
        app.model.import_files(os.path.join(directory, name) for name in ["work.vpn", "wgs_client-4", "home.WG"])
        app.wait_until(lambda: len(app.store.profiles) == 3, "three profiles")
        self.assertEqual([p.kind for p in app.store.profiles], [1, 2, 2])
        self.assertEqual(app.dialogs.import_reports, [])

    def test_reports_what_went_wrong_per_file(self):
        app = self.make_app()
        directory = self.files({
            "fine.ovpn": "client\nremote vpn.example.net 1194\n",
            "scripted.ovpn": "client\nremote vpn.example.net 1194\nup /bin/sh\n",
            "bad.ovpn": "client\n# fake: reject\n",
            "missing.ovpn": "client\nca nowhere.crt\n",
            "notes.txt": "client\n# fake: reject\n",
        })
        names = ["fine.ovpn", "scripted.ovpn", "bad.ovpn", "missing.ovpn", "notes.txt", "gone.ovpn"]
        app.model.import_files(os.path.join(directory, name) for name in names)
        app.wait_until(lambda: app.dialogs.import_reports, "the report")

        report = app.dialogs.import_reports[0]
        self.assertTrue(report.needs_attention)
        self.assertEqual([o.filename for o in report.outcomes], names)
        fine, scripted, bad, missing, notes, gone = report.outcomes
        self.assertTrue(fine.imported)
        self.assertEqual(fine.warnings, ())
        self.assertTrue(scripted.imported)
        self.assertEqual([w.directive for w in scripted.warnings], ["up"])
        self.assertFalse(bad.imported)
        self.assertIn("rejected", bad.failure, "the daemon's reason is shown")
        self.assertIn("nowhere.crt", missing.failure)
        # The extension decides nothing: the daemon judged the content of notes.txt.
        self.assertIn("rejected", notes.failure)
        self.assertFalse(gone.imported)
        # Only the two that the daemon accepted exist.
        self.assertEqual([p.name for p in app.store.profiles], ["fine", "scripted"])

    def test_a_failed_import_selects_nothing(self):
        app = self.make_app()
        directory = self.files({"bad.ovpn": "client\n# fake: reject\n"})
        app.model.import_files([os.path.join(directory, "bad.ovpn")])
        app.wait_until(lambda: app.dialogs.import_reports, "the report")
        self.assertIsNone(app.model.selection)
        self.assertEqual(len(app.store.profiles), 0)

    def test_an_auth_user_pass_file_becomes_saved_credentials_and_the_first_connection_does_not_ask(self):
        saved = InMemoryCredentialStore()
        app = self.make_app(saved)
        directory = self.files({
            "router.ovpn": "client\nremote vpn.example.net 1194\n# fake: needs-credentials\nauth-user-pass creds.txt\n",
            "creds.txt": "alice\ns3cret\n",
        })
        app.model.import_files([os.path.join(directory, "router.ovpn")])
        app.wait_until(lambda: len(app.store.profiles) == 1, "the profile")

        self.assertEqual(app.dialogs.import_reports, [], "a clean import needs no dialog")
        profile_id = app.store.profiles[0].id
        self.assertEqual(
            saved.lookup(profile_id, CredentialKind.USER_PASSWORD), Credentials(username="alice", password="s3cret")
        )
        app.store.set_enabled(True, profile_id)

        def connected():
            self.assertEqual(app.store.credential_prompts, [], "the dialog was shown although the credentials came with the profile")
            return app.store.profile(profile_id).state == ProfileState.CONNECTED

        app.wait_until(connected, "the connection")

    def test_a_profile_that_names_a_file_outside_its_folder_is_refused_and_nothing_is_stored(self):
        saved = InMemoryCredentialStore()
        app = self.make_app(saved)
        directory = self.files({"router.ovpn": "client\nremote vpn.example.net 1194\nauth-user-pass /etc/hosts\n"})
        app.model.import_files([os.path.join(directory, "router.ovpn")])
        app.wait_until(lambda: app.dialogs.import_reports, "the report")
        outcome = app.dialogs.import_reports[0].outcomes[0]
        self.assertFalse(outcome.imported)
        self.assertIn("/etc/hosts", outcome.failure, "the refused file is named")
        self.assertEqual(len(app.store.profiles), 0)

    def test_the_picker_imports_what_it_returns(self):
        app = self.make_app()
        directory = self.files({"office.ovpn": "client\nremote vpn.example.net 1194\n"})
        app.model.choose_import()
        self.assertEqual(len(app.platform.file_picker.requests), 1)
        app.platform.file_picker.choose([os.path.join(directory, "office.ovpn")])
        app.wait_until(lambda: len(app.store.profiles) == 1, "the profile")

    def test_nothing_is_picked_while_the_helper_does_not_answer(self):
        app = self.make_app()
        self.daemon.kill()
        app.wait_until(lambda: app.store.connection is Connection.UNAVAILABLE, "the outage")
        app.model.choose_import()
        self.assertEqual(app.platform.file_picker.requests, [])


class ManagementTests(AppTestCase):
    def test_deletion_asks_first_and_then_selects_another_profile(self):
        app = self.make_app()
        a = self.import_fixture(app, "a.ovpn")
        b = self.import_fixture(app, "b.ovpn")
        app.model.select(Selection.profile(b))

        app.dialogs.answers = [False]
        app.model.request_deletion(b)
        confirmation = app.dialogs.confirmations[-1]
        self.assertEqual(confirmation.title, "Delete “b”?")
        self.assertEqual(confirmation.action, "Delete")
        app.scheduler.settle(0.2)
        self.assertEqual(len(app.store.profiles), 2, "nothing is deleted before the confirmation")

        app.model.request_deletion(b)
        app.wait_until(lambda: app.store.profile(b) is None, "the profile to go")
        app.wait_until(lambda: app.model.selection == Selection.profile(a), "another profile to be selected")

        # The last one: nothing is left to select.
        app.model.delete(a)
        app.wait_until(lambda: len(app.store.profiles) == 0, "the last profile to go")
        app.wait_until(lambda: app.model.selection is None, "an empty selection")

    def test_reconciling_keeps_a_selection_that_still_exists(self):
        app = self.make_app()
        a = self.import_fixture(app, "a.ovpn")
        self.import_fixture(app, "b.ovpn")
        app.model.select(Selection.profile(a))
        app.model.reconcile_selection()
        self.assertEqual(app.model.selection, Selection.profile(a))
        for page in (DIAGNOSTICS, SETTINGS):
            app.model.select(page)
            app.model.reconcile_selection()
            self.assertEqual(app.model.selection, page)
        app.model.select(None)
        app.model.reconcile_selection()
        self.assertEqual(app.model.selection, Selection.profile(app.store.profiles[0].id), "something is selected as soon as there is something")

    def test_dragging_a_profile_sets_the_priority_order(self):
        app = self.make_app()
        a, b, c = (self.import_fixture(app, name) for name in ("a.ovpn", "b.ovpn", "c.ovpn"))
        app.model.move([2], 0)
        app.wait_until(
            lambda: app.store.profiles.ids == [c, a, b] and [p.settings.priority for p in app.store.profiles] == [1, 2, 3],
            "c first",
        )
        app.model.move_by(c, 1)
        app.wait_until(lambda: app.store.profiles.ids == [a, c, b], "c second")

        # Past the end nothing happens, and nothing is reported.
        app.model.move_by(b, 1)
        app.model.move_by(a, -1)
        app.scheduler.settle(0.3)
        self.assertEqual(app.dialogs.alerts, [])
        self.assertEqual(app.store.profiles.ids, [a, c, b])

    def test_renaming_trims_and_ignores_nothing_new(self):
        app = self.make_app()
        profile_id = self.import_fixture(app, "office.ovpn")
        app.model.rename(profile_id, "  Head Office \n")
        app.wait_until(lambda: app.store.profile(profile_id).name == "Head Office", "the rename")

        app.model.rename(profile_id, "   ")
        app.model.rename(profile_id, "Head Office")
        app.scheduler.settle(0.3)
        self.assertEqual(app.store.profile(profile_id).name, "Head Office")
        self.assertEqual(app.dialogs.alerts, [])

    def test_a_refused_rename_says_so_so_the_field_can_go_back(self):
        app = self.make_app()
        profile_id = self.import_fixture(app, "office.ovpn")
        answers = []
        app.model.rename(profile_id, "Head Office", answers.append)
        app.wait_until(lambda: answers, "the first answer")
        app.wait_until(lambda: app.store.profile(profile_id).name == "Head Office", "the rename")
        app.model.rename(profile_id, " Head Office ", answers.append)
        app.model.rename(profile_id, "   ", answers.append)
        app.wait_until(lambda: len(answers) == 3, "the next answers")
        # The name it has already is not a refusal; an empty name is not applied.
        self.assertEqual(answers, [True, True, False])

        self.daemon.kill()
        app.wait_until(lambda: app.store.connection is Connection.UNAVAILABLE, "the outage")
        app.model.rename(profile_id, "Other", answers.append)
        app.wait_until(lambda: len(answers) == 4, "the failed rename")
        self.assertFalse(answers[-1])
        self.assertEqual(app.dialogs.alerts[-1][0], "Save failed")

    def test_changing_one_setting_keeps_the_others(self):
        app = self.make_app()
        profile_id = self.import_fixture(app, "office.ovpn")
        app.model.change_settings(profile_id, lambda settings: setattr(settings, "auto_connect", True))
        app.wait_until(lambda: app.store.profile(profile_id).settings.auto_connect, "auto-connect")
        app.model.change_settings(profile_id, lambda settings: setattr(settings, "tunnel_mode", 3))
        app.wait_until(lambda: app.store.profile(profile_id).settings.tunnel_mode == 3, "split tunnel")
        self.assertTrue(app.store.profile(profile_id).settings.auto_connect)
        self.assertEqual(app.store.profile(profile_id).settings.priority, 1)

    def test_a_call_that_fails_becomes_an_alert(self):
        app = self.make_app()
        profile_id = self.import_fixture(app, "office.ovpn")
        self.assertEqual(app.dialogs.alerts, [])
        self.daemon.kill()
        app.wait_until(lambda: app.store.connection is Connection.UNAVAILABLE, "the outage")
        app.model.set_enabled(True, profile_id)
        app.wait_until(lambda: app.dialogs.alerts, "the alert")
        self.assertEqual(app.dialogs.alerts[0], ("Connect failed", "Helper unavailable"))

    def test_the_first_profile_is_selected_as_soon_as_it_appears(self):
        app = self.make_app()
        self.assertIsNone(app.model.selection)
        profile_id = self.import_fixture(app, "office.ovpn")
        app.wait_until(lambda: app.model.selection == Selection.profile(profile_id), "the selection")

    def test_pages_are_remembered_and_the_first_profile_opens_for_a_page(self):
        app = self.make_app()
        profile_id = self.import_fixture(app, "office.ovpn")
        app.model.select(DIAGNOSTICS)
        self.assertTrue(app.model.has_pages)
        app.model.show_section(ProfileSection.LOGS)
        self.assertEqual(app.model.selection, Selection.profile(profile_id))
        self.assertEqual(app.model.profile_section, ProfileSection.LOGS)
        app.model.select(SETTINGS)
        self.assertFalse(app.model.has_pages)
        app.model.show_diagnostics_page(DiagnosticsPage.HELPER_LOG)
        self.assertEqual(app.model.diagnostics_page, DiagnosticsPage.HELPER_LOG)


class DiagnosticsTests(DaemonTestCase):
    """The stale route of the fake daemon is removed once; this class has the daemon to itself."""

    def make_app(self):
        from support import App

        app = App(self.daemon.socket_path).start()
        self.addCleanup(app.close)
        return app

    def test_reads_diagnostics_and_refreshes_after_a_command(self):
        app = self.make_app()
        diagnostics = DiagnosticsModel(app.store, app.strings, app.scheduler)
        self.addCleanup(diagnostics.stop)
        diagnostics.start()
        app.wait_until(lambda: diagnostics.diagnostics is not None, "the first reading")
        self.assertEqual([stale.key for stale in diagnostics.diagnostics.stale_routes], ["fake-stale-1"])

        done = []
        app.model.remove_stale_route("fake-stale-1", lambda: done.append(True))
        app.wait_until(lambda: done, "the removal")
        diagnostics.refresh()
        self.assertEqual(list(diagnostics.diagnostics.stale_routes), [])
        self.assertEqual(app.dialogs.alerts, [])

        # Removing it again finds it gone already, which is what was asked for.
        done.clear()
        app.model.remove_stale_route("fake-stale-1", lambda: done.append(True))
        app.wait_until(lambda: done, "the second removal")
        self.assertEqual(app.dialogs.alerts, [], "an alert for a route that is already gone")

        resynced = []
        app.model.resync(lambda: resynced.append(True))
        app.wait_until(lambda: resynced, "the resync")
        diagnostics.refresh()
        self.assertEqual(diagnostics.diagnostics.network.last_change_reason, "manual")

    def test_removing_a_route_asks_first(self):
        app = self.make_app()
        route = app.client.get_diagnostics().stale_routes[0]
        app.dialogs.answers = [False]
        app.model.request_remove_stale_route(route)
        confirmation = app.dialogs.confirmations[-1]
        self.assertEqual(confirmation.title, f"Remove route {route.prefix}?")
        app.scheduler.settle(0.2)
        self.assertEqual(len(app.client.get_diagnostics().stale_routes), 1)

    def test_a_read_that_fails_keeps_the_last_reading_and_says_why(self):
        app = self.make_app()
        diagnostics = DiagnosticsModel(app.store, app.strings, app.scheduler)
        diagnostics.refresh()
        self.assertIsNotNone(diagnostics.diagnostics)
        self.daemon.kill()
        diagnostics.refresh()
        self.assertEqual(diagnostics.failure, "Helper unavailable")
        self.assertIsNotNone(diagnostics.diagnostics)
        self.daemon.start()


class HelperTests(AppTestCase):
    """The helper is a systemd unit the app starts and restarts; the fake stands for systemd."""

    def make_helper_app(self, registration, **options):
        app = self.make_app(is_overridden=False, app_version="0.0.0-dev", **options)
        app.platform.helper.current = registration
        return app

    def test_tracks_the_helper_through_its_states(self):
        app = self.make_helper_app(HelperRegistration.RUNNING)
        app.wait_until(lambda: app.model.setup.state is SetupState.READY, "a ready helper")

        self.daemon.kill()
        app.wait_until(lambda: app.store.connection is Connection.UNAVAILABLE, "the outage")
        app.model.refresh_helper()
        # Running, not answering: the profiles stay in view if there are any; here there are none.
        app.wait_until(lambda: app.model.setup.state is SetupState.NOT_RESPONDING, "not responding")

        # A retry waits for the daemon for a while before giving up on it.
        app.model.retry()
        self.assertEqual(app.model.setup.state, SetupState.CONNECTING)

        app.platform.helper.current = HelperRegistration.NOT_INSTALLED
        app.model.refresh_helper()
        app.model._end_settling()
        self.assertEqual(app.model.setup.state, SetupState.NOT_INSTALLED)

        app.platform.helper.current = HelperRegistration.STOPPED
        app.model.refresh_helper()
        self.assertEqual(app.model.setup.state, SetupState.NOT_RUNNING)

    def test_a_stopped_helper_is_started_through_systemd(self):
        app = self.make_helper_app(HelperRegistration.STOPPED)
        self.daemon.kill()
        app.wait_until(lambda: app.store.connection is Connection.UNAVAILABLE, "the outage")
        app.model.refresh_helper()
        app.wait_until(lambda: app.model.setup.state is SetupState.NOT_RUNNING, "a stopped helper")

        app.model.perform(SetupAction.START_HELPER)
        app.wait_until(lambda: app.platform.helper.calls == ["start"], "the start")
        app.wait_until(lambda: app.model.setup.state is SetupState.CONNECTING, "the wait for the helper")
        self.assertEqual(app.dialogs.alerts, [])

    def test_a_helper_that_cannot_be_started_is_reported(self):
        app = self.make_helper_app(HelperRegistration.STOPPED)
        app.platform.helper.fails = True
        app.model.start_helper()
        app.wait_until(lambda: app.dialogs.alerts, "the alert")
        self.assertEqual(app.dialogs.alerts[0], ("Helper failed", "Interactive authentication required."))

    def test_restarting_the_helper_asks_first_because_it_drops_every_tunnel(self):
        app = self.make_helper_app(HelperRegistration.RUNNING)
        app.dialogs.answers = [False]
        app.model.perform(SetupAction.RESTART_HELPER)
        confirmation = app.dialogs.confirmations[-1]
        self.assertEqual((confirmation.title, confirmation.message, confirmation.action), ("Restart helper?", "Connected profiles disconnect.", "Restart"))
        app.scheduler.settle(0.2)
        self.assertEqual(app.platform.helper.calls, [], "the helper was restarted before the confirmation")

        app.model.perform(SetupAction.RESTART_HELPER)
        app.wait_until(lambda: app.platform.helper.calls == ["restart"], "the restart")

    def test_a_helper_of_another_version_is_out_of_date(self):
        app = self.make_helper_app(HelperRegistration.RUNNING)
        app.wait_until(lambda: app.store.daemon_info is not None, "the daemon's version")
        app.model.app_version = "9.9.9"
        self.assertEqual(app.model.setup.state, SetupState.VERSION_MISMATCH)
        self.assertEqual(app.model.setup.daemon_version, app.store.daemon_info.version)
        self.assertIsNone(app.model.setup_content)
        self.assertTrue(app.model.setup.is_usable)

    def test_the_helper_is_managed_only_when_it_is_a_unit_of_the_app(self):
        app = self.make_helper_app(HelperRegistration.RUNNING)
        app.model.refresh_helper()
        self.assertTrue(app.model.can_manage_helper)
        app.platform.helper.current = HelperRegistration.NOT_INSTALLED
        app.model.refresh_helper()
        self.assertFalse(app.model.can_manage_helper)
        development = self.make_app(is_overridden=True)
        self.assertFalse(development.model.can_manage_helper)

    def test_launch_at_login_goes_through_the_login_item(self):
        app = self.make_app()
        app.model.set_launch_at_login(True)
        app.wait_until(lambda: app.model.launches_at_login, "launch at login")


class ProfilesInViewTests(AppTestCase):
    def test_profiles_stay_in_view_while_the_helper_does_not_answer(self):
        app = self.make_app(is_overridden=False)
        app.platform.helper.current = HelperRegistration.RUNNING
        self.import_fixture(app, "office.ovpn")
        self.daemon.kill()
        app.wait_until(lambda: app.store.connection is Connection.UNAVAILABLE, "the outage")
        app.model.refresh_helper()
        app.model._end_settling()
        app.wait_until(lambda: app.model.setup.state is SetupState.NOT_RESPONDING, "not responding")
        self.assertTrue(app.model.keeps_profiles_in_view)
        self.assertIsNone(app.model.setup_content)
        self.assertEqual(app.model.switched_on_profiles, [], "a helper that does not answer says nothing true about its profiles")


class QuitTests(AppTestCase):
    def test_profiles_that_are_on_are_what_quitting_leaves_running(self):
        app = self.make_app()
        office = self.import_fixture(app, "office.ovpn")
        lab = self.import_fixture(app, "lab.ovpn")
        self.import_fixture(app, "idle.ovpn")
        self.assertEqual(app.model.switched_on_profiles, [], "nothing is on, so Quit has nothing to ask")

        app.store.set_enabled(True, office)
        app.store.set_enabled(True, lab)
        app.wait_until(lambda: all(app.store.profile(i).state == ProfileState.CONNECTED for i in (office, lab)), "both connected")
        self.assertEqual({p.id for p in app.model.switched_on_profiles}, {office, lab})

        result = []
        app.model.disconnect_all(result.append)
        app.wait_until(lambda: result, "the stop")
        self.assertEqual(result, [True])
        app.wait_until(lambda: all(app.store.profile(i).state == ProfileState.DISCONNECTED for i in (office, lab)), "both disconnected")
        self.assertEqual(app.model.switched_on_profiles, [])
        self.assertEqual(app.dialogs.alerts, [])

    def test_quit_asks_what_to_do_with_the_profiles_that_are_on(self):
        app = self.make_app()
        office = self.import_fixture(app, "office.ovpn")
        proceeded, blocked = [], []

        app.model.request_quit(lambda: proceeded.append(1), lambda: blocked.append(1))
        self.assertEqual((proceeded, app.platform.quit_prompts.asked), ([1], []), "nothing is on, so nothing is asked")

        app.store.set_enabled(True, office)
        self.wait_for_state(app, office, ProfileState.CONNECTED)
        app.wait_until(lambda: app.model.switched_on_profiles, "the profile to be on")

        app.platform.quit_prompts.answer = QuitAnswer.QUIT
        app.model.request_quit(lambda: proceeded.append(2), lambda: blocked.append(2))
        self.assertEqual(proceeded, [1, 2])
        self.assertEqual(app.store.profile(office).state, ProfileState.CONNECTED, "Quit leaves the tunnels up")

        app.platform.quit_prompts.answer = QuitAnswer.CANCEL
        app.model.request_quit(lambda: proceeded.append(3), lambda: blocked.append(3))
        self.assertEqual((proceeded, blocked), ([1, 2], [3]))

        app.platform.quit_prompts.answer = QuitAnswer.DISCONNECT_AND_QUIT
        app.model.request_quit(lambda: proceeded.append(4), lambda: blocked.append(4))
        app.wait_until(lambda: 4 in proceeded, "the quit after the disconnection")
        self.assertEqual(app.store.profile(office).desired_enabled, False)

    def test_unsaved_edits_are_asked_about_before_anything_else(self):
        app = self.make_app()
        profile_id = self.import_fixture(app, "home.conf", wireguard_profile())
        editor = app.model.editor(app.store.profile(profile_id))
        editor.load()
        app.wait_until(lambda: editor.phase.value == "ready", "the editor")
        editor.set_text(editor.text + "# unsaved\n")
        self.assertTrue(app.model.has_unsaved_edits)

        proceeded, blocked = [], []
        app.platform.quit_prompts.discard_edits = False
        app.model.request_quit(lambda: proceeded.append(1), lambda: blocked.append(1))
        self.assertEqual((proceeded, blocked, app.platform.quit_prompts.asked), ([], [1], ["discard edits"]))

        app.platform.quit_prompts.discard_edits = True
        app.model.request_quit(lambda: proceeded.append(2), lambda: blocked.append(2))
        self.assertEqual(proceeded, [2])


class CredentialDialogTests(AppTestCase):
    def test_a_profile_that_asks_gets_a_dialog_and_the_answer_connects_it(self):
        app = self.make_app()
        profile_id = self.import_fixture(app, "router.ovpn", NEEDS_CREDENTIALS)
        app.model.set_enabled(True, profile_id)
        request = app.wait_until(lambda: app.dialogs.credential_requests.get(profile_id), "the dialog")
        self.assertEqual(request.profile_name, "router")
        self.assertEqual(request.prompt.kind, CredentialKind.USER_PASSWORD)
        self.assertTrue(app.model.needs_window)

        request.submit("alice", "s3cret")
        self.wait_for_state(app, profile_id, ProfileState.CONNECTED)
        app.wait_until(lambda: profile_id in app.dialogs.dismissed, "the dialog to go")
        self.assertFalse(app.model.needs_window)

    def test_answers_the_daemon_could_not_take_ask_again(self):
        app = self.make_app()
        profile_id = self.import_fixture(app, "router.ovpn", NEEDS_CREDENTIALS)
        app.model.set_enabled(True, profile_id)
        first = app.wait_until(lambda: app.dialogs.credential_requests.get(profile_id), "the dialog")

        real = app.client.provide_credentials
        calls = []

        def refuse_once(*args, **kwargs):
            calls.append(args)
            if len(calls) == 1:
                raise DaemonFailure(FailureKind.UNAVAILABLE, "")
            return real(*args, **kwargs)

        app.client.provide_credentials = refuse_once
        first.submit("alice", "s3cret")
        app.wait_until(lambda: app.dialogs.alerts, "the alert")
        self.assertEqual(app.dialogs.alerts[0][0], "Connect failed")
        second = app.wait_until(
            lambda: (r := app.dialogs.credential_requests.get(profile_id)) is not None and r is not first and r, "a new dialog"
        )
        second.submit("alice", "s3cret")
        self.wait_for_state(app, profile_id, ProfileState.CONNECTED)

    def test_cancelling_the_dialog_stops_connecting(self):
        app = self.make_app()
        profile_id = self.import_fixture(app, "router.ovpn", NEEDS_CREDENTIALS)
        app.model.set_enabled(True, profile_id)
        request = app.wait_until(lambda: app.dialogs.credential_requests.get(profile_id), "the dialog")
        request.cancel()
        self.wait_for_state(app, profile_id, ProfileState.DISCONNECTED)
        self.assertFalse(app.store.profile(profile_id).desired_enabled)

    def test_a_dialog_is_asked_for_once_and_dismissed_when_the_profile_stops_asking(self):
        app = self.make_app()
        profile_id = self.import_fixture(app, "router.ovpn", NEEDS_CREDENTIALS)
        app.model.set_enabled(True, profile_id)
        app.wait_until(lambda: profile_id in app.dialogs.credential_requests, "the dialog")
        asked = app.dialogs.credential_requests[profile_id]
        app.scheduler.settle(0.3)
        self.assertIs(app.dialogs.credential_requests[profile_id], asked)
        app.store.set_enabled(False, profile_id)
        app.wait_until(lambda: profile_id not in app.dialogs.credential_requests, "the dialog to be dismissed")

    def test_a_keyring_that_cannot_save_is_an_alert_not_a_silence(self):
        from test_profile_store import FailingSaveStore

        app = self.make_app(FailingSaveStore())
        profile_id = self.import_fixture(app, "router.ovpn", NEEDS_CREDENTIALS)
        app.model.set_enabled(True, profile_id)
        request = app.wait_until(lambda: app.dialogs.credential_requests.get(profile_id), "the dialog")
        with self.assertLogs("plaitway", "ERROR"):
            request.submit("alice", "s3cret")
            app.wait_until(lambda: app.dialogs.alerts, "the alert")
        self.assertEqual(app.dialogs.alerts[0], ("Credentials not saved", "Secret Service unavailable"))
        self.wait_for_state(app, profile_id, ProfileState.CONNECTED)


class UserMessageTests(unittest.TestCase):
    def setUp(self):
        from plaitway.l10n.strings import Strings

        self.strings = Strings("en")

    def test_daemon_failures_are_worded(self):
        message = lambda failure: user_message(failure, self.strings)
        self.assertEqual(message(DaemonFailure(FailureKind.PERMISSION_DENIED)), "Administrator required")
        self.assertEqual(message(DaemonFailure(FailureKind.UNAVAILABLE, "details")), "Helper unavailable")
        self.assertEqual(message(DaemonFailure(FailureKind.NOT_FOUND)), "Profile not found")
        self.assertEqual(message(DaemonFailure(FailureKind.REJECTED, "line 3: nope")), "line 3: nope")
        self.assertEqual(message(DaemonFailure(FailureKind.OTHER, "odd")), "odd")

    def test_import_failures_name_the_file_that_was_refused(self):
        from plaitway.client import profile_importer as importer

        message = lambda failure: user_message(failure, self.strings)
        self.assertIn("/x/ca.crt", message(importer.MissingFile("ca", "/x/ca.crt")))
        outside = message(importer.OutsideProfileDirectory("key", "../id_ed25519"))
        self.assertTrue("../id_ed25519" in outside and "key" in outside)
        self.assertIn("/x/creds.txt", message(importer.NotCredentials("/x/creds.txt")))
        self.assertIn("/x/big", message(importer.TooLarge("/x/big")))


if __name__ == "__main__":
    unittest.main()
