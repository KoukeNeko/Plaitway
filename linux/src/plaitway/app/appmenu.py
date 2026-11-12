"""The menu bar of the window for a desktop that shows it outside the window (the global menu of
KDE and Unity, and GNOME with an extension for it).

GTK 4 does not export one. The menu is a com.canonical.dbusmenu on the session bus, and the desktop's
com.canonical.AppMenu.Registrar is told which window it belongs to. The registrar knows windows by
their X11 id, so under Wayland (no id) the window keeps the menu of its header bar only.
"""

from __future__ import annotations

import logging

import gi

gi.require_version("Gtk", "4.0")
gi.require_version("Gio", "2.0")
from gi.repository import Gio, GLib, Gtk  # noqa: E402

from ..core.app_model import AppModel  # noqa: E402
from ..l10n.strings import Strings  # noqa: E402
from .tray.dbusmenu import DBusMenu, MenuEntry  # noqa: E402

log = logging.getLogger(__name__)

REGISTRAR = "com.canonical.AppMenu.Registrar"
REGISTRAR_PATH = "/com/canonical/AppMenu/Registrar"


def menu_spec(strings: Strings) -> list[tuple[str, list[tuple[str, str | None]]]]:
    """The menus of the window, shared by its menu bar and by the global menu: each title and its entries as
    (label, the name of the action that it does), None for a divider."""
    s = strings
    return [
        (s.file, [(s.import_profile, "app.import"), (s.disconnect_all, "app.disconnect-all"), ("", None), (s.quit_plaitway, "app.quit")]),
        (s.view, [(s.routing, "win.routing"), (s.diagnostics, "win.diagnostics"), (s.open_settings, "app.settings")]),
        (s.help, [(s.about_plaitway, "app.about")]),
    ]


def menu_model(strings: Strings) -> Gio.Menu:
    """`menu_spec` as the model of a menu bar in the window."""
    bar = Gio.Menu()
    for title, items in menu_spec(strings):
        menu = Gio.Menu()
        section = Gio.Menu()
        for label, action in items:
            if action is None:
                menu.append_section(None, section)
                section = Gio.Menu()
            else:
                section.append(label, action)
        menu.append_section(None, section)
        bar.append_submenu(title, menu)
    return bar


def window_id(window: Gtk.Window) -> int | None:
    """The X11 id of the window, or None under Wayland or where GdkX11 is not installed."""
    surface = window.get_surface()
    try:
        gi.require_version("GdkX11", "4.0")
        from gi.repository import GdkX11
    except (ValueError, ImportError):
        return None
    return surface.get_xid() if isinstance(surface, GdkX11.X11Surface) else None


class AppMenu:
    """File, View and Help of the window, kept in step with what the app can do."""

    def __init__(self, window: Gtk.Window, application: Gtk.Application, model: AppModel) -> None:
        self._window = window
        self._application = application
        self._model = model
        self._menu: DBusMenu | None = None
        self._xid: int | None = None
        self._connection: Gio.DBusConnection | None = None
        self._watch = 0
        self._unfollow = model.changed.connect(self._update)
        window.connect("map", lambda *_: self._start())
        window.connect("destroy", lambda *_: self._stop())

    def _start(self) -> None:
        xid = window_id(self._window)
        if xid is None or self._menu is not None:
            return
        try:
            self._connection = Gio.bus_get_sync(Gio.BusType.SESSION, None)
        except GLib.Error as error:
            log.debug("no session bus for the global menu: %s", error.message)
            return
        self._xid = xid
        self._menu = DBusMenu(f"/com/canonical/menu/{xid:x}")
        self._menu.export(self._connection)
        self._update()
        # The registrar may start after the app, or again after it ended.
        self._watch = Gio.bus_watch_name_on_connection(
            self._connection, REGISTRAR, Gio.BusNameWatcherFlags.NONE, self._registrar_appeared, None
        )

    def _stop(self) -> None:
        self._unfollow()
        if self._watch:
            Gio.bus_unwatch_name(self._watch)
            self._watch = 0
        if self._connection is not None and self._xid is not None:
            self._call("UnregisterWindow", GLib.Variant("(u)", (self._xid,)))
        if self._menu is not None:
            self._menu.unexport()
            self._menu = None

    def _registrar_appeared(self, connection, name, owner) -> None:
        self._call("RegisterWindow", GLib.Variant("(uo)", (self._xid, f"/com/canonical/menu/{self._xid:x}")))

    def _call(self, method: str, parameters: GLib.Variant) -> None:
        self._connection.call(
            REGISTRAR, REGISTRAR_PATH, REGISTRAR, method, parameters, None, Gio.DBusCallFlags.NONE, -1, None,
            self._call_done, method,
        )

    @staticmethod
    def _call_done(connection, result, method: str) -> None:
        try:
            connection.call_finish(result)
        except GLib.Error as error:
            log.debug("the global menu registrar refused %s: %s", method, error.message)

    def _update(self) -> None:
        if self._menu is None:
            return
        self._menu.set_entries([
            MenuEntry(
                title,
                children=tuple(
                    MenuEntry.divider() if action is None else MenuEntry(label, self._enabled(action), on_activate=self._activator(action))
                    for label, action in items
                ),
            )
            for title, items in menu_spec(self._model.strings)
        ])

    def _enabled(self, name: str) -> bool:
        scope, _, action = name.partition(".")
        action_map = self._window if scope == "win" else self._application
        found = action_map.lookup_action(action)
        return found is None or found.get_enabled()

    def _activator(self, name: str):
        return lambda: self._window.activate_action(name, None)
