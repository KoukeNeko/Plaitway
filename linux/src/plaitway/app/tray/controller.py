"""The tray item of the app, kept in step with the models; and whether there is a
desktop tray to show it in."""

from __future__ import annotations

import logging
from collections.abc import Callable
from dataclasses import dataclass

import gi

gi.require_version("Gio", "2.0")
from gi.repository import Gio, GLib  # noqa: E402

from ...core.aggregate import AggregateState
from ...core.app_model import AppModel
from ...core.ports import Cancel
from ...core.tray_model import Mark, TrayAction, TrayMenu
from ..paths import APP_ID, APP_NAME
from .dbusmenu import DBusMenu, MenuEntry
from .status_notifier import WATCHER_NAME, StatusNotifierItem

log = logging.getLogger(__name__)

CHECK = {Mark.OFF: 0, Mark.ON: 1, Mark.MIXED: -1}


@dataclass(frozen=True)
class TrayCommands:
    """What the menu's rows do besides the profiles'."""

    open_window: Callable[[], None]
    import_profile: Callable[[], None]
    open_settings: Callable[[], None]
    disconnect_all: Callable[[], None]
    quit: Callable[[], None]


class TrayController:
    """Shows the tray item while a StatusNotifierWatcher owns its name. Stock GNOME has
    none (an extension provides it): then there is no tray, and the app does without."""

    def __init__(
        self,
        model: AppModel,
        commands: TrayCommands,
        icon_theme_path: str,
        on_availability: Callable[[bool], None],
        connection: Gio.DBusConnection | None = None,
    ) -> None:
        """connection: the bus to use; the session bus unless a test gives its own."""
        self._model = model
        self._commands = commands
        self._on_availability = on_availability
        self._menu = DBusMenu()
        self._item = StatusNotifierItem(APP_ID, APP_NAME, icon_theme_path, self._menu, commands.open_window)
        self._connection = connection
        self._watch = 0
        self._wanted = False
        self._watcher_present = False
        self._available = False
        self._exported = False
        self._disconnect: Cancel | None = None
        self._shown: tuple | None = None

    @property
    def available(self) -> bool:
        """The item is registered with a tray that shows it."""
        return self._available

    @property
    def menu(self) -> DBusMenu:
        return self._menu

    # MARK: Life

    def start(self) -> None:
        """Watches for a tray; the item is shown while there is one and `set_wanted(True)`."""
        try:
            self._connection = self._connection or Gio.bus_get_sync(Gio.BusType.SESSION, None)
        except GLib.Error as error:
            log.warning("no session bus, no tray: %s", error.message)
            self._on_availability(False)
            return
        self._watch = Gio.bus_watch_name_on_connection(
            self._connection, WATCHER_NAME, Gio.BusNameWatcherFlags.NONE, self._watcher_appeared, self._watcher_vanished
        )
        self._disconnect = self._model.changed.connect(self.update)

    def stop(self) -> None:
        if self._disconnect is not None:
            self._disconnect()
        if self._watch:
            Gio.bus_unwatch_name(self._watch)
            self._watch = 0
        self._unexport()

    def set_wanted(self, wanted: bool) -> None:
        """The person's setting: Show Tray Icon."""
        self._wanted = wanted
        if wanted and self._watcher_present and not self._exported:
            self._export_and_register()
        elif not wanted:
            self._unexport()
            self._set_available(False)

    # MARK: The watcher

    def _watcher_appeared(self, connection, name, owner) -> None:
        self._watcher_present = True
        if self._wanted:
            self._export_and_register()

    def _watcher_vanished(self, connection, name) -> None:
        self._watcher_present = False
        self._unexport()
        self._set_available(False)

    def _export_and_register(self) -> None:
        if self._exported or self._connection is None:
            return
        self._item.export(self._connection)
        self._exported = True
        self._shown = None
        self.update()
        self._item.register_with_watcher(self._registered)

    def _registered(self, accepted: bool) -> None:
        self._set_available(accepted)
        if not accepted:
            self._unexport()

    def _unexport(self) -> None:
        if self._exported:
            self._item.unexport()
            self._exported = False

    def _set_available(self, available: bool) -> None:
        if available != self._available:
            self._available = available
            self._on_availability(available)

    # MARK: The models

    def update(self) -> None:
        """The icon, the tooltip and the rows, from the models."""
        if not self._exported:
            return
        model = self._model
        strings = model.strings
        setup = model.setup
        profiles = list(model.store.profiles)
        aggregate = AggregateState.of(setup, profiles)
        menu = TrayMenu.of(setup, profiles, strings)
        shown = (aggregate, menu)
        if shown == self._shown:
            return
        self._shown = shown
        self._item.show(aggregate.tray_icon, aggregate.label(strings))
        self._menu.set_entries(self._entries(menu))

    def _entries(self, menu: TrayMenu) -> list[MenuEntry]:
        s = self._model.strings
        entries: list[MenuEntry] = []
        if menu.status:
            entries.append(MenuEntry(menu.status, enabled=False))
        if menu.summary:
            entries.append(MenuEntry(menu.summary, enabled=False))
        for item in menu.items:
            entries.append(MenuEntry(
                f"{item.title} — {item.subtitle}",
                enabled=item.is_enabled,
                check=CHECK[item.mark],
                on_activate=lambda item=item: self._profile_chosen(item.profile_id, item.action),
            ))
        if menu.can_disconnect_all:
            entries.append(MenuEntry.divider())
            entries.append(MenuEntry(s.disconnect_all, on_activate=self._commands.disconnect_all))
        if entries:
            entries.append(MenuEntry.divider())
        entries.append(MenuEntry(s.open_plaitway, on_activate=self._commands.open_window))
        entries.append(MenuEntry(s.import_profile, enabled=menu.can_import, on_activate=self._commands.import_profile))
        entries.append(MenuEntry(s.open_settings, on_activate=self._commands.open_settings))
        entries.append(MenuEntry.divider())
        entries.append(MenuEntry(s.quit_plaitway, on_activate=self._commands.quit))
        return entries

    def _profile_chosen(self, profile_id: str, action: TrayAction) -> None:
        """A row does what its profile needs: the menu has no buttons."""
        match action:
            case TrayAction.CONNECT | TrayAction.RETRY:
                self._model.set_enabled(True, profile_id)
            case TrayAction.DISCONNECT:
                self._model.set_enabled(False, profile_id)
            case TrayAction.ANSWER_CREDENTIALS:
                self._commands.open_window()
