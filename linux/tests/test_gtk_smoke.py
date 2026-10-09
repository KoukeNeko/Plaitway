"""Builds every page of the window against the daemon and looks at what GTK says about
it: no warnings, and a name for every control that a person can act on."""

import time
import unittest

import gi

gi.require_version("Gtk", "4.0")
gi.require_version("Adw", "1")
from gi.repository import Adw, Gdk, GLib, Gtk  # noqa: E402

from plaitway.app import style  # noqa: E402
from plaitway.app.widgets import ACCESSIBLE_NAME  # noqa: E402
from plaitway.client.types import ProfileState  # noqa: E402
from plaitway.core.app_model import DIAGNOSTICS, SETTINGS, DiagnosticsPage, ProfileSection, Selection  # noqa: E402
from plaitway.core.app_state import WindowGeometry  # noqa: E402
from plaitway.core.ports import HelperRegistration  # noqa: E402
from plaitway.core.profile_store import Connection  # noqa: E402

from support import NEEDS_CREDENTIALS, AppTestCase, openvpn_profile, wireguard_profile  # noqa: E402

DISPLAY = Gdk.Display.get_default()
GTK_DOMAINS = {"Gtk", "Gdk", "Adwaita", "GLib-GObject", "Gsk", "Pango"}
MESSAGES: list[str] = []


def _record(level, fields, n_fields, user_data):
    """Keeps what the toolkit complains about: GTK does not raise, it logs."""
    values = {field.key: field.value for field in fields}
    domain = values.get("GLIB_DOMAIN", b"")
    domain = domain.decode() if isinstance(domain, bytes) else str(domain)
    if domain in GTK_DOMAINS and level & (GLib.LogLevelFlags.LEVEL_CRITICAL | GLib.LogLevelFlags.LEVEL_WARNING | GLib.LogLevelFlags.LEVEL_ERROR):
        message = values.get("MESSAGE", b"")
        MESSAGES.append(f"{domain}: {message.decode() if isinstance(message, bytes) else message}")
    return GLib.LogWriterOutput.UNHANDLED


GLib.log_set_writer_func(_record, None)

# What a person can act on. Each needs a name: text of its own, a tooltip, or an accessible label.
ACTIONABLE = (
    Gtk.Button, Gtk.ToggleButton, Gtk.MenuButton, Adw.SplitButton, Gtk.DropDown, Gtk.SearchEntry,
    Gtk.Switch, Gtk.CheckButton, Gtk.TextView,
)
# Parts of widgets that name themselves: the window's own buttons, and what a row or a split button builds.
SELF_NAMING_ANCESTORS = (
    Gtk.WindowControls, Adw.EntryRow, Adw.ExpanderRow, Adw.SplitButton, Gtk.MenuButton, Gtk.Popover, Adw.Banner,
    # libadwaita 1.7; before it the page switcher is made of toggle buttons, which have text of their own.
    *((Adw.ToggleGroup,) if hasattr(Adw, "ToggleGroup") else ()),
)
# libadwaita's own: the back button of a collapsed split view.
SELF_NAMING_TYPES = {"AdwBackButton"}


def ancestors(widget):
    parent = widget.get_parent()
    while parent is not None:
        yield parent
        parent = parent.get_parent()


def descendants(widget):
    child = widget.get_first_child()
    while child is not None:
        yield child
        yield from descendants(child)
        child = child.get_next_sibling()


def has_name(widget) -> bool:
    if getattr(widget, ACCESSIBLE_NAME, None) or widget.get_tooltip_text():
        return True
    if isinstance(widget, Adw.SplitButton):
        return bool(widget.get_label())
    if isinstance(widget, (Gtk.Button, Gtk.ToggleButton, Gtk.MenuButton)):
        if isinstance(widget, Gtk.Button) and widget.get_label():
            return True
        if isinstance(widget, Gtk.MenuButton) and widget.get_label():
            return True
        child = widget.get_child() if hasattr(widget, "get_child") else None
        if isinstance(child, Adw.ButtonContent) and child.get_label():
            return True
        return any(isinstance(d, Gtk.Label) and d.get_text() for d in descendants(widget))
    if isinstance(widget, Gtk.SearchEntry):
        return bool(widget.get_placeholder_text())
    if isinstance(widget, Gtk.Switch):
        return any(isinstance(a, Adw.PreferencesRow) and a.get_title() for a in ancestors(widget))
    return False


_application: list = []


def gtk_application() -> Adw.Application:
    """One application for the whole run: the id can be registered on the bus once."""
    if not _application:
        application = Adw.Application(application_id="io.github.koukeneko.Plaitway.Tests")
        application.register(None)
        _application.append(application)
    return _application[0]


class SmokeTestCase(AppTestCase):
    @classmethod
    def setUpClass(cls):
        if DISPLAY is None:
            raise unittest.SkipTest("there is no display to open a window on")
        super().setUpClass()
        Adw.init()
        style.install()
        cls.gtk_app = gtk_application()

    def setUp(self):
        super().setUp()
        MESSAGES.clear()

    def open_window(self, app, width=1040, height=660):
        from plaitway.app.window import MainWindow

        window = MainWindow(self.gtk_app, app.model, WindowGeometry(width, height))
        self.addCleanup(window.destroy)
        window.present()
        self.pump(app)
        return window

    def pump(self, app, seconds=0.4):
        context = GLib.MainContext.default()
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            while context.iteration(False):
                pass
            app.scheduler.run_pending()
            time.sleep(0.01)

    def three_profiles(self, app):
        office = self.import_fixture(app, "office.ovpn")
        home = self.import_fixture(app, "home.conf", wireguard_profile())
        broken = self.import_fixture(app, "broken.ovpn", openvpn_profile(markers=["# fake: fail"]))
        for profile_id in (office, home, broken):
            app.store.set_enabled(True, profile_id)
        self.wait_for_state(app, office, ProfileState.CONNECTED)
        self.wait_for_state(app, broken, ProfileState.FAILED)
        return office, home, broken

    def unnamed(self, window):
        offenders = []
        for widget in descendants(window):
            if not isinstance(widget, ACTIONABLE) or has_name(widget):
                continue
            if any(isinstance(a, SELF_NAMING_ANCESTORS) or a.__gtype__.name in SELF_NAMING_TYPES for a in ancestors(widget)):
                continue
            chain = " < ".join(a.__gtype__.name for a in ancestors(widget))
            offenders.append(f"{widget.__gtype__.name} {widget.get_css_classes()} in {chain}")
        return offenders


class WindowTests(SmokeTestCase):
    def test_every_page_of_every_profile_builds_without_a_warning(self):
        app = self.make_app()
        window = self.open_window(app)
        ids = self.three_profiles(app)
        for profile_id in ids:
            app.model.select(Selection.profile(profile_id))
            for section in ProfileSection:
                app.model.show_section(section)
                self.pump(app, 0.2)
                self.assertEqual(window._current_stack.get_visible_child_name(), section.name.lower())
        for page in DiagnosticsPage:
            app.model.select(DIAGNOSTICS)
            app.model.show_diagnostics_page(page)
            self.pump(app, 0.5)
        app.model.select(SETTINGS)
        self.pump(app)
        self.assertEqual(MESSAGES, [])

    def test_every_control_a_person_can_act_on_has_a_name(self):
        app = self.make_app()
        window = self.open_window(app)
        ids = self.three_profiles(app)
        for profile_id in ids:
            app.model.select(Selection.profile(profile_id))
            for section in ProfileSection:
                app.model.show_section(section)
                self.pump(app, 0.2)
        app.model.select(DIAGNOSTICS)
        self.pump(app, 0.8)
        app.model.select(SETTINGS)
        self.pump(app)
        # A profile that asks for credentials, and one in the narrow layout.
        self.assertEqual(self.unnamed(window), [])

    def test_the_narrow_layout_has_a_bar_of_pages_and_a_named_sidebar(self):
        app = self.make_app()
        window = self.open_window(app, 400, 720)
        office, _, _ = self.three_profiles(app)
        app.model.select(Selection.profile(office))
        self.pump(app, 0.5)
        self.assertTrue(window._split.get_collapsed())
        self.assertTrue(window._switcher_bar.get_reveal())
        self.assertTrue(app.model.compact)
        self.assertEqual(self.unnamed(window), [])
        # Every page fits the narrowest window, which is what the minimum width promises.
        for section in ProfileSection:
            app.model.show_section(section)
            self.pump(app, 0.3)
            self.assertLessEqual(window.measure(Gtk.Orientation.HORIZONTAL, -1)[0], 360, section)
        app.model.select(DIAGNOSTICS)
        for page in DiagnosticsPage:
            app.model.show_diagnostics_page(page)
            self.pump(app, 0.4)
            self.assertLessEqual(window.measure(Gtk.Orientation.HORIZONTAL, -1)[0], 360, page)
        app.model.select(SETTINGS)
        self.pump(app, 0.3)
        self.assertLessEqual(window.measure(Gtk.Orientation.HORIZONTAL, -1)[0], 360)
        self.assertEqual(MESSAGES, [])

    def test_the_sidebar_lists_the_profiles_in_priority_order_with_their_state(self):
        app = self.make_app()
        window = self.open_window(app)
        office, home, broken = self.three_profiles(app)
        self.pump(app, 0.5)
        rows = []
        row = window._sidebar._profiles.get_first_child()
        while row is not None:
            rows.append(getattr(row, ACCESSIBLE_NAME))
            row = row.get_next_sibling()
        self.assertEqual(rows, ["office, OpenVPN · Connected", "home, WireGuard · Connected", "broken, OpenVPN · Failed"])

    def test_the_state_is_shown_by_a_glyph_and_a_word_not_by_colour_alone(self):
        app = self.make_app()
        window = self.open_window(app)
        office, home, broken = self.three_profiles(app)
        self.pump(app, 0.5)
        glyphs = []
        row = window._sidebar._profiles.get_first_child()
        while row is not None:
            glyphs.append((row.glyph.get_icon_name(), row.caption.get_text()))
            row = row.get_next_sibling()
        self.assertEqual(len({glyph for glyph, _ in glyphs}), 2)
        self.assertEqual(
            glyphs,
            [("plaitway-state-connected-symbolic", "OpenVPN · Connected"),
             ("plaitway-state-connected-symbolic", "WireGuard · Connected"),
             ("plaitway-state-attention-symbolic", "OpenVPN · Failed")],
        )

    def test_every_icon_the_app_names_exists_in_the_icon_theme(self):
        from plaitway.core import presentation

        theme = Gtk.IconTheme.get_for_display(DISPLAY)
        from plaitway.app import paths

        if paths.from_checkout():
            theme.add_search_path(str(paths.data_dir() / "icons"))
        names = set(presentation.PROFILE_GLYPHS.values()) | set(presentation.ROUTE_GLYPHS.values())
        names |= {"go-up-symbolic", "go-down-symbolic", "edit-copy-symbolic", "go-bottom-symbolic", "view-reveal-symbolic",
                  "view-conceal-symbolic", "list-add-symbolic", "open-menu-symbolic", "dialog-warning-symbolic",
                  "dialog-error-symbolic", "dialog-information-symbolic", "changes-prevent-symbolic",
                  "applications-engineering-symbolic", "text-x-generic-symbolic", "network-workgroup-symbolic",
                  "view-list-symbolic", "document-edit-symbolic", "emblem-system-symbolic", "preferences-system-symbolic"}
        missing = sorted(name for name in names if not theme.has_icon(name))
        self.assertEqual(missing, [])


class ActionTests(SmokeTestCase):
    """What a person does with the sidebar and the keyboard."""

    def rows(self, window):
        rows = {}
        row = window._sidebar._profiles.get_first_child()
        while row is not None:
            rows[row.profile_id] = row
            row = row.get_next_sibling()
        return rows

    def test_dropping_a_profile_below_another_sets_the_priority_order(self):
        app = self.make_app()
        window = self.open_window(app)
        a, b, c = (self.import_fixture(app, name) for name in ("a.ovpn", "b.ovpn", "c.ovpn"))
        self.pump(app, 0.4)
        rows = self.rows(window)
        height = rows[a].get_height()
        # c is dropped on the upper half of a: it goes before it.
        self.assertTrue(window._sidebar._dropped(rows[a], c, height * 0.25))
        app.wait_until(lambda: app.store.profiles.ids == [c, a, b], "the new order")
        self.pump(app, 0.4)
        rows = self.rows(window)
        # and a on the lower half of b: after it.
        self.assertTrue(window._sidebar._dropped(rows[b], a, height * 0.75))
        app.wait_until(lambda: app.store.profiles.ids == [c, b, a], "the second order")

    def test_the_context_menu_acts_on_the_profile_it_was_opened_on(self):
        app = self.make_app()
        window = self.open_window(app)
        a = self.import_fixture(app, "a.ovpn")
        b = self.import_fixture(app, "b.ovpn")
        self.pump(app, 0.4)
        window._sidebar._popup_menu(self.rows(window)[b], 10, 10)
        self.pump(app, 0.2)
        window.lookup_action("profile-toggle").activate(GLib.Variant.new_string(b))
        self.wait_for_state(app, b, ProfileState.CONNECTED)
        window.lookup_action("profile-move-up").activate(GLib.Variant.new_string(b))
        app.wait_until(lambda: app.store.profiles.ids == [b, a], "b to move up")
        window.lookup_action("profile-delete").activate(GLib.Variant.new_string(a))
        self.assertEqual(app.dialogs.confirmations[-1].title, "Delete “a”?")
        app.wait_until(lambda: app.store.profile(a) is None, "a to be deleted")
        self.assertEqual(MESSAGES, [])

    def test_the_keyboard_shortcuts_reach_the_actions(self):
        app = self.make_app()
        window = self.open_window(app)
        profile_id = self.import_fixture(app, "office.ovpn")
        app.wait_until(lambda: app.model.selection == Selection.profile(profile_id), "the selection")
        self.pump(app, 0.3)

        self.assertTrue(window.activate_action("win.toggle-connection", None))  # Ctrl+K
        self.wait_for_state(app, profile_id, ProfileState.CONNECTED)
        window.activate_action("win.show-page", GLib.Variant.new_int32(2))  # Ctrl+3
        self.assertEqual(app.model.profile_section, ProfileSection.LOGS)
        self.pump(app, 0.3)
        window.activate_action("win.find", None)  # Ctrl+F
        self.assertEqual(app.model.search_request, 1)
        window.activate_action("win.diagnostics", None)  # Ctrl+Shift+D
        self.assertEqual(app.model.selection, DIAGNOSTICS)
        window.activate_action("win.show-page", GLib.Variant.new_int32(1))  # Ctrl+2
        self.assertEqual(app.model.diagnostics_page, DiagnosticsPage.HELPER_LOG)

    def test_the_accelerators_of_the_app_are_the_ones_the_readme_lists(self):
        from plaitway.app.application import ACCELERATORS

        self.assertEqual(ACCELERATORS["win.toggle-connection"], ["<Control>k"])
        self.assertEqual(ACCELERATORS["app.disconnect-all"], ["<Control><Shift>k"])
        self.assertEqual(ACCELERATORS["app.import"], ["<Control>i"])
        self.assertEqual(ACCELERATORS["win.find"], ["<Control>f"])
        self.assertEqual(ACCELERATORS["app.settings"], ["<Control>comma"])
        self.assertEqual(ACCELERATORS["app.quit"], ["<Control>q"])
        self.assertEqual([ACCELERATORS[f"win.show-page(int32 {i})"] for i in range(5)], [[f"<Control>{i + 1}"] for i in range(5)])


class SetupTests(SmokeTestCase):
    def test_the_window_shows_the_helper_setup_while_the_daemon_does_not_answer(self):
        app = self.make_app(is_overridden=False)
        app.platform.helper.current = HelperRegistration.STOPPED
        window = self.open_window(app)
        self.daemon.kill()
        app.wait_until(lambda: app.store.connection is Connection.UNAVAILABLE, "the outage")
        app.model.refresh_helper()
        app.model._end_settling()
        self.pump(app, 0.5)
        self.assertEqual(window._root.get_visible_child_name(), "setup")
        buttons = [d for d in descendants(window._setup) if isinstance(d, Gtk.Button)]
        self.assertEqual([button.get_label() for button in buttons], ["Start Helper"])
        self.assertEqual(self.unnamed(window), [])

    def test_a_window_with_no_profile_offers_to_import(self):
        app = self.make_app()
        window = self.open_window(app)
        self.pump(app, 0.5)
        self.assertEqual(window._content.get_visible_child_name(), "empty")

    def test_pressing_connect_connects_the_selected_profile(self):
        app = self.make_app()
        window = self.open_window(app)
        profile_id = self.import_fixture(app, "office.ovpn")
        app.wait_until(lambda: app.model.selection == Selection.profile(profile_id), "the selection")
        self.pump(app, 0.4)
        window._actions._toggle.emit("clicked")
        self.wait_for_state(app, profile_id, ProfileState.CONNECTED)
        self.pump(app, 0.4)
        self.assertEqual(window._actions._toggle.get_label(), "Disconnect")

    def test_a_credential_prompt_does_not_leave_a_dialog_behind_when_it_goes_away(self):
        app = self.make_app()
        self.open_window(app)
        profile_id = self.import_fixture(app, "router.ovpn", NEEDS_CREDENTIALS)
        app.model.set_enabled(True, profile_id)
        app.wait_until(lambda: profile_id in app.dialogs.credential_requests, "the dialog")
        app.store.set_enabled(False, profile_id)
        app.wait_until(lambda: profile_id not in app.dialogs.credential_requests, "the dialog to go")


if __name__ == "__main__":
    unittest.main()
