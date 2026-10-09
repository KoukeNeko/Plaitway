"""The store against the real daemon: what it shows, what it does with an outage,
and how it answers a profile that asks for a password."""

import time
import unittest

from plaitway.client.errors import DaemonFailure, FailureKind
from plaitway.client.types import CredentialKind, Credentials, ProfileSettings, ProfileState
from plaitway.core.credentials import InMemoryCredentialStore
from plaitway.core.ports import CredentialStoreError
from plaitway.core.profile_store import Connection

from support import NEEDS_CREDENTIALS, AppTestCase, openvpn_profile, wireguard_profile


class ProfileStoreTests(AppTestCase):
    def test_starts_empty_and_knows_the_daemon(self):
        app = self.make_app()
        self.assertEqual(len(app.store.profiles), 0)
        app.wait_until(lambda: app.store.daemon_info is not None, "the daemon's description")
        info = app.store.daemon_info
        self.assertTrue(info.privileged)
        self.assertEqual([engine.available for engine in info.engines], [True, True])
        self.assertTrue(info.version)

    def test_connects_several_profiles_at_the_same_time(self):
        app = self.make_app()
        office = self.import_fixture(app, "office.ovpn")
        lab = self.import_fixture(app, "lab.ovpn")
        home = self.import_fixture(app, "home.conf", wireguard_profile())
        for profile_id in (office, lab, home):
            app.store.set_enabled(True, profile_id)
        app.wait_until(
            lambda: all(app.store.profile(i).state == ProfileState.CONNECTED for i in (office, lab, home)),
            "three connected profiles",
        )
        self.assertTrue(app.store.profile(home).status.interface_name)

    def test_reports_failure_with_an_error(self):
        app = self.make_app()
        broken = self.import_fixture(app, "broken.ovpn", openvpn_profile(markers=["# fake: fail"]))
        app.store.set_enabled(True, broken)
        self.wait_for_state(app, broken, ProfileState.FAILED)
        self.assertTrue(app.store.profile(broken).last_error)

    def test_disabling_disconnects(self):
        app = self.make_app()
        office = self.import_fixture(app, "office.ovpn")
        app.store.set_enabled(True, office)
        self.wait_for_state(app, office, ProfileState.CONNECTED)
        app.store.set_enabled(False, office)
        self.wait_for_state(app, office, ProfileState.DISCONNECTED)
        self.assertFalse(app.store.profile(office).desired_enabled)

    def test_an_unknown_profile_is_not_found(self):
        app = self.make_app()
        with self.assertRaises(DaemonFailure) as raised:
            app.store.set_enabled(True, "nope")
        self.assertEqual(raised.exception.kind, FailureKind.NOT_FOUND)

    def test_reorders_and_settles_on_the_last_event(self):
        app = self.make_app()
        a, b, c = (self.import_fixture(app, name) for name in ("a.ovpn", "b.ovpn", "c.conf"))
        app.wait_until(lambda: len(app.store.profiles) == 3, "three profiles")
        self.assertEqual(app.store.profiles.ids, [a, b, c])

        app.store.reorder([c, a, b])
        # The daemon reports the new priorities one profile at a time; the store settles on the last.
        app.wait_until(
            lambda: app.store.profiles.ids == [c, a, b] and [p.settings.priority for p in app.store.profiles] == [1, 2, 3],
            "the new order",
        )
        app.store.reorder([b, c, a])
        app.wait_until(lambda: app.store.profiles.ids == [b, c, a], "the second order")

    def test_listeners_hear_of_changes_on_the_ui_thread(self):
        app = self.make_app()
        heard = []
        app.store.changed.connect(lambda: heard.append(len(app.store.profiles)))
        self.import_fixture(app, "office.ovpn")
        app.wait_until(lambda: 1 in heard, "the change")


class OutageTests(AppTestCase):
    def test_recovers_when_the_daemon_restarts(self):
        app = self.make_app()
        office = self.import_fixture(app, "office.ovpn")
        app.store.set_enabled(True, office)
        self.wait_for_state(app, office, ProfileState.CONNECTED)

        self.daemon.kill()
        app.wait_until(lambda: app.store.connection is Connection.UNAVAILABLE, "the outage to be noticed")
        # The store keeps the last known profiles instead of emptying the UI.
        self.assertEqual(app.store.profile(office).state, ProfileState.CONNECTED)
        self.assertEqual(app.store.failure.kind, FailureKind.UNAVAILABLE)

        self.daemon.start()
        app.wait_until(lambda: app.store.connection is Connection.CONNECTED, "the store to reconnect")
        # The profile survives in the state directory; its tunnel does not.
        self.assertEqual(app.store.profile(office).state, ProfileState.DISCONNECTED)
        self.assertFalse(app.store.profile(office).desired_enabled)

    def test_profiles_survive_a_restart_of_the_daemon_in_their_order(self):
        app = self.make_app()
        a = self.import_fixture(app, "a.ovpn")
        b = self.import_fixture(app, "b.ovpn")
        app.store.reorder([b, a])
        app.wait_until(lambda: app.store.profiles.ids == [b, a], "the order")

        self.daemon.kill()
        app.wait_until(lambda: app.store.connection is Connection.UNAVAILABLE, "the outage")
        self.daemon.start()
        app.wait_until(lambda: app.store.connection is Connection.CONNECTED, "the reconnect")
        self.assertEqual(app.store.profiles.ids, [b, a])

    def test_launching_the_app_does_not_reconnect_what_the_user_disconnected(self):
        app = self.make_app()
        flagged = self.import_fixture(app, "flagged.ovpn", settings=ProfileSettings(auto_connect=True))
        # The daemon connects auto-connect profiles when it starts; once the user disconnects one, it stays off.
        self.assertEqual(app.store.profile(flagged).state, ProfileState.DISCONNECTED)

        launched = self.make_app()
        app.scheduler.settle(0.5)
        self.assertEqual(launched.store.profile(flagged).state, ProfileState.DISCONNECTED)
        self.assertFalse(launched.store.profile(flagged).desired_enabled)


class SlowRemovalStore(InMemoryCredentialStore):
    """A credential store that is slow to remove an item, like a keyring that stops to ask."""

    def remove(self, profile_id, kind):
        time.sleep(0.3)
        super().remove(profile_id, kind)


class FailingSaveStore(InMemoryCredentialStore):
    def save(self, credentials, profile_id, kind):
        raise CredentialStoreError("no Secret Service", unavailable=True)


class CredentialFlowTests(AppTestCase):
    def prompt_for(self, app, profile_id):
        app.wait_until(lambda: app.store.credential_prompts, "the credential prompt")
        prompt = app.store.credential_prompts[0]
        self.assertEqual(prompt.profile_id, profile_id)
        return prompt

    def test_asks_once_and_then_answers_from_the_credential_store(self):
        saved = InMemoryCredentialStore()
        app = self.make_app(saved)
        profile_id = self.import_fixture(app, "router.ovpn", NEEDS_CREDENTIALS)

        app.store.set_enabled(True, profile_id)
        prompt = self.prompt_for(app, profile_id)
        self.assertEqual(prompt.kind, CredentialKind.USER_PASSWORD)
        self.assertFalse(prompt.rejected)

        app.store.provide_credentials(profile_id, "alice", "s3cret")
        self.wait_for_state(app, profile_id, ProfileState.CONNECTED)
        self.assertEqual(app.store.credential_prompts, [])
        self.assertEqual(
            saved.lookup(profile_id, CredentialKind.USER_PASSWORD), Credentials(username="alice", password="s3cret")
        )

        # The next connection needs no dialog: the store answers by itself.
        app.store.set_enabled(False, profile_id)
        self.wait_for_state(app, profile_id, ProfileState.DISCONNECTED)
        app.store.set_enabled(True, profile_id)

        def reconnected():
            self.assertEqual(app.store.credential_prompts, [], "the dialog was shown although the credentials were saved")
            return app.store.profile(profile_id).state == ProfileState.CONNECTED

        app.wait_until(reconnected, "the reconnection")

    def test_asks_again_when_the_daemon_refuses_what_was_typed(self):
        saved = InMemoryCredentialStore()
        app = self.make_app(saved)
        profile_id = self.import_fixture(app, "router.ovpn", NEEDS_CREDENTIALS)
        app.store.set_enabled(True, profile_id)
        self.prompt_for(app, profile_id)

        # The fake daemon refuses the password "wrong" once.
        app.store.provide_credentials(profile_id, "alice", "wrong")
        app.wait_until(lambda: app.store.credential_prompts and app.store.credential_prompts[0].rejected, "the prompt to come back")
        self.assertEqual(app.store.credential_prompts[0].message, "authentication failed")
        self.assertIsNone(saved.lookup(profile_id, CredentialKind.USER_PASSWORD), "refused credentials were kept")

        app.store.provide_credentials(profile_id, "alice", "right")
        self.wait_for_state(app, profile_id, ProfileState.CONNECTED)
        self.assertEqual(app.store.credential_prompts, [])
        self.assertEqual(saved.lookup(profile_id, CredentialKind.USER_PASSWORD).password, "right")

    def test_forgets_saved_credentials_that_the_daemon_refuses_and_asks(self):
        saved = InMemoryCredentialStore()
        app = self.make_app(saved)
        profile_id = self.import_fixture(app, "router.ovpn", NEEDS_CREDENTIALS)
        saved.save(Credentials(username="alice", password="wrong"), profile_id, CredentialKind.USER_PASSWORD)

        app.store.set_enabled(True, profile_id)
        # The saved password is sent without asking, refused, and then the dialog appears.
        app.wait_until(
            lambda: app.store.credential_prompts and app.store.credential_prompts[0].rejected, "the dialog after the refusal"
        )
        self.assertIsNone(saved.lookup(profile_id, CredentialKind.USER_PASSWORD))

    def test_uses_credentials_saved_before_the_first_connection(self):
        saved = InMemoryCredentialStore()
        app = self.make_app(saved)
        profile_id = self.import_fixture(app, "router.ovpn", NEEDS_CREDENTIALS)
        saved.save(Credentials(username="alice", password="s3cret"), profile_id, CredentialKind.USER_PASSWORD)

        app.store.set_enabled(True, profile_id)

        def connected():
            self.assertEqual(app.store.credential_prompts, [])
            return app.store.profile(profile_id).state == ProfileState.CONNECTED

        app.wait_until(connected, "the connection")

    def test_cancelling_the_dialog_stops_connecting(self):
        app = self.make_app()
        profile_id = self.import_fixture(app, "router.ovpn", NEEDS_CREDENTIALS)
        app.store.set_enabled(True, profile_id)
        self.prompt_for(app, profile_id)

        app.store.cancel_credentials(profile_id)
        self.wait_for_state(app, profile_id, ProfileState.DISCONNECTED)
        self.assertEqual(app.store.credential_prompts, [])
        self.assertFalse(app.store.profile(profile_id).desired_enabled)

    def test_the_dialog_goes_away_when_someone_else_disconnects(self):
        app = self.make_app()
        profile_id = self.import_fixture(app, "router.ovpn", NEEDS_CREDENTIALS)
        app.store.set_enabled(True, profile_id)
        self.prompt_for(app, profile_id)
        app.store.set_enabled(False, profile_id)
        app.wait_until(lambda: not app.store.credential_prompts, "the prompt to go")

    def test_the_dialog_goes_away_with_the_daemon(self):
        app = self.make_app()
        profile_id = self.import_fixture(app, "router.ovpn", NEEDS_CREDENTIALS)
        app.store.set_enabled(True, profile_id)
        self.prompt_for(app, profile_id)

        self.daemon.kill()
        app.wait_until(lambda: app.store.connection is Connection.UNAVAILABLE, "the outage")
        self.assertEqual(app.store.credential_prompts, [])

    def test_answering_without_a_request_is_refused(self):
        app = self.make_app()
        profile_id = self.import_fixture(app, "router.ovpn")
        with self.assertRaises(DaemonFailure) as raised:
            app.store.provide_credentials(profile_id, "alice", "s3cret")
        self.assertEqual(raised.exception.kind, FailureKind.REJECTED)

    def test_deleting_a_profile_forgets_its_credentials(self):
        saved = InMemoryCredentialStore()
        app = self.make_app(saved)
        profile_id = self.import_fixture(app, "router.ovpn", NEEDS_CREDENTIALS)
        saved.save(Credentials(username="alice", password="s3cret"), profile_id, CredentialKind.USER_PASSWORD)
        saved.save(Credentials(password="phrase"), profile_id, CredentialKind.KEY_PASSPHRASE)
        other = self.import_fixture(app, "other.ovpn")
        saved.save(Credentials(username="bob", password="pw"), other, CredentialKind.USER_PASSWORD)

        app.store.delete_profile(profile_id)
        self.assertFalse(saved.has(profile_id, CredentialKind.USER_PASSWORD))
        self.assertFalse(saved.has(profile_id, CredentialKind.KEY_PASSPHRASE))
        self.assertIsNotNone(saved.lookup(other, CredentialKind.USER_PASSWORD))

    def test_keeps_the_keyring_in_step_with_a_profile_another_client_deletes(self):
        saved = InMemoryCredentialStore()
        app = self.make_app(saved)
        profile_id = self.import_fixture(app, "router.ovpn", NEEDS_CREDENTIALS)
        saved.save(Credentials(username="alice", password="s3cret"), profile_id, CredentialKind.USER_PASSWORD)

        self.client.delete_profile(profile_id)  # the command line, say
        app.wait_until(lambda: app.store.profile(profile_id) is None, "the profile to go")
        app.wait_until(lambda: not saved.has(profile_id, CredentialKind.USER_PASSWORD), "the saved credentials to go")

    def test_deleting_a_profile_forgets_its_credentials_before_it_returns(self):
        # The removed event may never arrive (the watch is down at that moment), so the
        # delete itself must leave nothing behind.
        saved = SlowRemovalStore()
        app = self.make_app(saved)
        profile_id = self.import_fixture(app, "router.ovpn", NEEDS_CREDENTIALS)
        saved.save(Credentials(username="alice", password="s3cret"), profile_id, CredentialKind.USER_PASSWORD)
        saved.save(Credentials(password="phrase"), profile_id, CredentialKind.KEY_PASSPHRASE)

        app.store.delete_profile(profile_id)
        self.assertFalse(saved.has(profile_id, CredentialKind.USER_PASSWORD))
        self.assertFalse(saved.has(profile_id, CredentialKind.KEY_PASSPHRASE))

    def test_saved_credentials_can_be_found_and_forgotten(self):
        saved = InMemoryCredentialStore()
        app = self.make_app(saved)
        profile_id = self.import_fixture(app, "router.ovpn", NEEDS_CREDENTIALS)
        self.assertFalse(app.store.has_saved_credentials(profile_id))
        saved.save(Credentials(username="alice", password="s3cret"), profile_id, CredentialKind.USER_PASSWORD)
        self.assertTrue(app.store.has_saved_credentials(profile_id))
        app.store.forget_saved_credentials(profile_id)
        self.assertFalse(app.store.has_saved_credentials(profile_id))

    def test_credentials_that_came_with_a_profile_are_saved_for_it(self):
        saved = InMemoryCredentialStore()
        app = self.make_app(saved)
        result = app.store.import_profile(
            NEEDS_CREDENTIALS, "router.ovpn", credentials=Credentials(username="alice", password="s3cret")
        )
        self.assertEqual(
            saved.lookup(result.profile.id, CredentialKind.USER_PASSWORD), Credentials(username="alice", password="s3cret")
        )

    def test_a_keyring_that_cannot_save_is_reported_and_the_connection_goes_on(self):
        app = self.make_app(FailingSaveStore())
        reported = []
        app.store.on_credentials_not_saved = reported.append
        profile_id = self.import_fixture(app, "router.ovpn", NEEDS_CREDENTIALS)
        app.store.set_enabled(True, profile_id)
        self.prompt_for(app, profile_id)

        with self.assertLogs("plaitway.core.profile_store", "ERROR"):
            app.store.provide_credentials(profile_id, "alice", "s3cret")
        self.wait_for_state(app, profile_id, ProfileState.CONNECTED)
        self.assertEqual(len(reported), 1)
        self.assertTrue(reported[0].unavailable)


if __name__ == "__main__":
    unittest.main()
