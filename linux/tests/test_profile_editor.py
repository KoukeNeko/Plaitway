import unittest

from plaitway.client.types import ProfileKind
from plaitway.core.profile_editor import Phase, ProfileEditor

from support import AppTestCase, openvpn_profile, wireguard_profile

# The private key of the WireGuard fixture.
PRIVATE_KEY = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="


class EditorTestCase(AppTestCase):
    def load(self, app, profile_id: str, kind: int) -> ProfileEditor:
        editor = ProfileEditor(profile_id, kind, app.store, app.strings, app.scheduler, app.runner)
        editor.load()
        app.wait_until(lambda: editor.phase is not Phase.LOADING, "the text")
        return editor

    def loaded_wireguard(self, app):
        profile_id = self.import_fixture(app, "home.conf", wireguard_profile())
        return self.load(app, profile_id, ProfileKind.WIREGUARD), profile_id

    def save(self, app, editor, *, reconnect=False, is_on=False):
        results = []
        editor.save(reconnect, is_on, results.append)
        app.wait_until(lambda: results, "the save")
        return results[0]


class ProfileEditorTests(EditorTestCase):
    def test_shows_the_stored_text_with_its_secrets_hidden(self):
        app = self.make_app()
        editor, _ = self.loaded_wireguard(app)
        self.assertEqual(editor.phase, Phase.READY)
        self.assertNotIn(PRIVATE_KEY, editor.text)
        self.assertIn("‹secret 1›", editor.text)
        self.assertIn("Address = 10.6.0.2/32", editor.text)
        self.assertFalse(editor.is_dirty, "a text nobody touched is not an edit")

    def test_saves_an_edit_without_losing_the_secret(self):
        app = self.make_app()
        editor, profile_id = self.loaded_wireguard(app)
        editor.set_text(editor.text.replace("10.6.0.2/32", "10.6.0.9/32"))
        self.assertTrue(editor.is_dirty)

        result = self.save(app, editor)

        self.assertTrue(result.saved)
        stored = app.store.profile_content(profile_id)
        self.assertIn("Address = 10.6.0.9/32", stored)
        self.assertIn(PRIVATE_KEY, stored, "the secret never left the daemon's text")
        self.assertNotIn(PRIVATE_KEY, editor.text)
        self.assertFalse(editor.is_dirty)
        self.assertIsNone(editor.diagnostic)
        self.assertFalse(editor.runs_old_text, "the profile is not on")

    def test_a_saved_profile_that_is_on_keeps_the_old_text_until_it_reconnects(self):
        app = self.make_app()
        editor, _ = self.loaded_wireguard(app)
        editor.set_text(editor.text + "# a note\n")
        self.assertTrue(self.save(app, editor, is_on=True).saved)
        self.assertTrue(editor.runs_old_text)

        editor.set_text(editor.text + "# another\n")
        self.assertTrue(self.save(app, editor, reconnect=True, is_on=True).saved)
        self.assertFalse(editor.runs_old_text, "a restart applies the text at once")

    def test_a_profile_that_connects_again_runs_the_saved_text(self):
        app = self.make_app()
        editor, _ = self.loaded_wireguard(app)
        editor.set_text(editor.text + "# a note\n")
        self.save(app, editor, is_on=True)
        self.assertTrue(editor.runs_old_text)
        editor.note_restart()
        self.assertFalse(editor.runs_old_text)

    def test_shows_the_secrets_on_request_and_hides_them_again_with_the_edits_made_meanwhile(self):
        app = self.make_app()
        editor, _ = self.loaded_wireguard(app)
        editor.toggle_secrets()
        self.assertTrue(editor.shows_secrets)
        self.assertIn(PRIVATE_KEY, editor.text)
        self.assertFalse(editor.is_dirty)

        editor.set_text(editor.text.replace("10.6.0.2/32", "10.6.0.7/32"))
        self.assertTrue(editor.is_dirty)

        editor.toggle_secrets()
        self.assertFalse(editor.shows_secrets)
        self.assertNotIn(PRIVATE_KEY, editor.text)
        self.assertIn("10.6.0.7/32", editor.text)
        self.assertTrue(editor.is_dirty, "the edit survives hiding the secrets")

    def test_a_secret_typed_over_is_stored_as_the_new_secret(self):
        app = self.make_app()
        editor, profile_id = self.loaded_wireguard(app)
        replacement = "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC="
        editor.set_text(editor.text.replace("‹secret 1›", replacement))
        self.assertTrue(self.save(app, editor).saved)
        stored = app.store.profile_content(profile_id)
        self.assertIn(f"PrivateKey = {replacement}", stored)
        self.assertNotIn(PRIVATE_KEY, stored)

    def test_a_refused_text_stays_in_the_editor_and_is_marked_at_its_line(self):
        app = self.make_app()
        profile_id = self.import_fixture(app, "office.ovpn", openvpn_profile())
        editor = self.load(app, profile_id, ProfileKind.OPENVPN)
        before = app.store.profile_content(profile_id)
        # The fake daemon refuses a text with this marker, naming the line it stands on.
        editor.set_text(editor.text + "# fake: reject\n")
        marker_line = len(editor.text.split("\n")) - 1

        result = self.save(app, editor)

        self.assertFalse(result.saved)
        self.assertIsNone(result.error)
        self.assertEqual(editor.diagnostic.line, marker_line)
        self.assertTrue(editor.diagnostic.message)
        self.assertTrue(editor.text.endswith("# fake: reject\n"), "the person's text is not taken away")
        self.assertTrue(editor.is_dirty)
        self.assertEqual(app.store.profile_content(profile_id), before, "a refused text changes nothing")

    def test_a_refusal_is_marked_at_the_line_the_person_sees(self):
        app = self.make_app()
        editor, _ = self.loaded_wireguard(app)
        # The key is one line in the editor and one in the text, so a refusal of the line
        # after it names the same line in both; the marker makes the fake daemon refuse.
        editor.set_text(editor.text.replace("DNS = 10.6.0.1", "# fake: reject"))
        line = editor.text.split("\n").index("# fake: reject") + 1
        self.assertFalse(self.save(app, editor).saved)
        self.assertEqual(editor.diagnostic.line, line)

    def test_a_placeholder_that_stands_twice_is_not_sent(self):
        app = self.make_app()
        editor, profile_id = self.loaded_wireguard(app)
        before = app.store.profile_content(profile_id)
        editor.set_text(editor.text + "# ‹secret 1›\n")

        result = self.save(app, editor)

        self.assertFalse(result.saved)
        self.assertEqual(editor.diagnostic.line, len(editor.text.split("\n")) - 1)
        self.assertEqual(app.store.profile_content(profile_id), before)
        # It cannot be shown either: a secret is in one place.
        editor.toggle_secrets()
        self.assertFalse(editor.shows_secrets)

    def test_revert_goes_back_to_the_stored_text(self):
        app = self.make_app()
        editor, _ = self.loaded_wireguard(app)
        original = editor.text
        editor.set_text(editor.text + "garbage\n")
        self.assertTrue(editor.is_dirty)
        editor.revert()
        self.assertEqual(editor.text, original)
        self.assertFalse(editor.is_dirty)
        self.assertIsNone(editor.diagnostic)

    def test_a_load_does_not_throw_away_edits(self):
        app = self.make_app()
        editor, _ = self.loaded_wireguard(app)
        editor.set_text(editor.text + "# unsaved\n")
        editor.load()
        app.scheduler.settle(0.3)
        self.assertTrue(editor.text.endswith("# unsaved\n"))

    def test_a_profile_that_is_gone_cannot_be_read(self):
        app = self.make_app()
        editor = self.load(app, "nope", ProfileKind.OPENVPN)
        self.assertEqual(editor.phase, Phase.UNAVAILABLE)
        self.assertEqual(editor.unavailable_message, "Profile not found")
        self.assertFalse(editor.is_dirty)

    def test_hides_the_secrets_again_when_the_page_is_left(self):
        app = self.make_app()
        editor, _ = self.loaded_wireguard(app)
        editor.toggle_secrets()
        editor.set_text(editor.text.replace("10.6.0.2/32", "10.6.0.8/32"))

        editor.hide_secrets()

        self.assertFalse(editor.shows_secrets)
        self.assertNotIn(PRIVATE_KEY, editor.text)
        self.assertIn("10.6.0.8/32", editor.text, "the edit made while they were shown stays")
        self.assertTrue(editor.is_dirty)
        # Nothing to hide: nothing happens.
        hidden = editor.text
        editor.hide_secrets()
        self.assertEqual(editor.text, hidden)

    def test_showing_or_hiding_the_secrets_clears_a_mark_that_named_a_line_of_the_other_view(self):
        app = self.make_app()
        profile_id = self.import_fixture(app, "office.ovpn", openvpn_profile())
        editor = self.load(app, profile_id, ProfileKind.OPENVPN)
        editor.set_text(editor.text + "# fake: reject\n")
        self.assertFalse(self.save(app, editor).saved)
        self.assertIsNotNone(editor.diagnostic)
        editor.toggle_secrets()
        self.assertIsNone(editor.diagnostic, "the line it named is not the same line any more")

    def test_a_failure_that_is_not_a_refusal_is_returned(self):
        app = self.make_app()
        editor, _ = self.loaded_wireguard(app)
        editor.set_text(editor.text + "# a note\n")
        self.daemon.kill()
        result = self.save(app, editor)
        self.assertFalse(result.saved)
        self.assertIsNotNone(result.error)
        self.assertTrue(editor.is_dirty)

    def test_the_model_knows_of_edits_that_are_not_saved(self):
        app = self.make_app()
        profile_id = self.import_fixture(app, "home.conf", wireguard_profile())
        editor = app.model.editor(app.store.profile(profile_id))
        self.assertFalse(app.model.has_unsaved_edits)
        editor.load()
        app.wait_until(lambda: editor.phase is Phase.READY, "the text")
        self.assertFalse(app.model.has_unsaved_edits, "reading the text is not an edit")
        editor.set_text(editor.text + "# unsaved\n")
        self.assertTrue(app.model.has_unsaved_edits)
        editor.revert()
        self.assertFalse(app.model.has_unsaved_edits)

    def test_the_model_keeps_one_editor_per_profile_until_the_profile_is_gone(self):
        app = self.make_app()
        profile_id = self.import_fixture(app, "home.conf", wireguard_profile())
        profile = app.store.profile(profile_id)
        self.assertIs(app.model.editor(profile), app.model.editor(profile))
        app.store.delete_profile(profile_id)
        app.wait_until(lambda: app.store.profile(profile_id) is None, "the profile to go")
        app.model.reconcile_selection()
        self.assertIsNot(app.model.editor(profile), None)


if __name__ == "__main__":
    unittest.main()
