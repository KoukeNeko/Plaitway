"""org.kde.StatusNotifierItem: the item itself, and its registration with the watcher."""

from __future__ import annotations

import logging
import os
from collections.abc import Callable

import gi

gi.require_version("Gio", "2.0")
from gi.repository import Gio, GLib  # noqa: E402

from .dbusmenu import OBJECT_PATH as MENU_PATH
from .dbusmenu import DBusMenu

log = logging.getLogger(__name__)

ITEM_PATH = "/StatusNotifierItem"
ITEM_INTERFACE = "org.kde.StatusNotifierItem"
WATCHER_NAME = "org.kde.StatusNotifierWatcher"
WATCHER_PATH = "/StatusNotifierWatcher"
WATCHER_INTERFACE = "org.kde.StatusNotifierWatcher"

INTROSPECTION = """
<node>
  <interface name="org.kde.StatusNotifierItem">
    <property name="Category" type="s" access="read"/>
    <property name="Id" type="s" access="read"/>
    <property name="Title" type="s" access="read"/>
    <property name="Status" type="s" access="read"/>
    <property name="WindowId" type="u" access="read"/>
    <property name="IconThemePath" type="s" access="read"/>
    <property name="Menu" type="o" access="read"/>
    <property name="ItemIsMenu" type="b" access="read"/>
    <property name="IconName" type="s" access="read"/>
    <property name="IconPixmap" type="a(iiay)" access="read"/>
    <property name="OverlayIconName" type="s" access="read"/>
    <property name="OverlayIconPixmap" type="a(iiay)" access="read"/>
    <property name="AttentionIconName" type="s" access="read"/>
    <property name="AttentionIconPixmap" type="a(iiay)" access="read"/>
    <property name="AttentionMovieName" type="s" access="read"/>
    <property name="ToolTip" type="(sa(iiay)ss)" access="read"/>
    <method name="ContextMenu"><arg type="i" name="x" direction="in"/><arg type="i" name="y" direction="in"/></method>
    <method name="Activate"><arg type="i" name="x" direction="in"/><arg type="i" name="y" direction="in"/></method>
    <method name="SecondaryActivate"><arg type="i" name="x" direction="in"/><arg type="i" name="y" direction="in"/></method>
    <method name="Scroll"><arg type="i" name="delta" direction="in"/><arg type="s" name="orientation" direction="in"/></method>
    <signal name="NewTitle"/>
    <signal name="NewIcon"/>
    <signal name="NewAttentionIcon"/>
    <signal name="NewOverlayIcon"/>
    <signal name="NewToolTip"/>
    <signal name="NewStatus"><arg type="s" name="status"/></signal>
  </interface>
</node>
"""


class StatusNotifierItem:
    """The tray item of the app. It is registered with the desktop's StatusNotifierWatcher
    and reports its state through the icon, the tooltip and the menu."""

    def __init__(
        self,
        app_id: str,
        title: str,
        icon_theme_path: str,
        menu: DBusMenu,
        on_activate: Callable[[], None],
    ) -> None:
        self._id = app_id
        self._title = title
        self._icon_theme_path = icon_theme_path
        self._menu = menu
        self._on_activate = on_activate
        self._icon = ""
        self._tooltip = ""
        self._connection: Gio.DBusConnection | None = None
        self._registration = 0
        self._name_owner = 0
        self._info = Gio.DBusNodeInfo.new_for_xml(INTROSPECTION).interfaces[0]
        self.bus_name = f"org.kde.StatusNotifierItem-{os.getpid()}-1"

    @property
    def registered(self) -> bool:
        return self._registration != 0

    def export(self, connection: Gio.DBusConnection) -> None:
        self._connection = connection
        self._registration = connection.register_object(ITEM_PATH, self._info, self._method_call, self._get_property, None)
        self._menu.export(connection, self._icon_theme_path)
        self._name_owner = Gio.bus_own_name_on_connection(
            connection, self.bus_name, Gio.BusNameOwnerFlags.NONE, None, None
        )

    def unexport(self) -> None:
        if self._connection is None:
            return
        self._menu.unexport()
        if self._registration:
            self._connection.unregister_object(self._registration)
        if self._name_owner:
            Gio.bus_unown_name(self._name_owner)
        self._registration = 0
        self._name_owner = 0
        self._connection = None

    def register_with_watcher(self, on_done: Callable[[bool], None]) -> None:
        """Asks the watcher to show the item. `on_done` gets whether it agreed."""
        if self._connection is None:
            on_done(False)
            return

        def finished(connection, result) -> None:
            try:
                connection.call_finish(result)
            except GLib.Error as error:
                log.info("the StatusNotifierWatcher refused the item: %s", error.message)
                on_done(False)
                return
            on_done(True)

        self._connection.call(
            WATCHER_NAME, WATCHER_PATH, WATCHER_INTERFACE, "RegisterStatusNotifierItem",
            GLib.Variant("(s)", (self.bus_name,)), None, Gio.DBusCallFlags.NONE, 5000, None, finished,
        )

    # MARK: State

    def show(self, icon: str, tooltip: str) -> None:
        """What the item looks like: one of the app's state icons, and the words for it."""
        icon_changed = icon != self._icon
        tooltip_changed = tooltip != self._tooltip
        self._icon, self._tooltip = icon, tooltip
        if icon_changed:
            self._emit("NewIcon")
        if tooltip_changed:
            self._emit("NewToolTip")

    def _emit(self, signal: str) -> None:
        if self._connection is not None:
            self._connection.emit_signal(None, ITEM_PATH, ITEM_INTERFACE, signal, None)

    # MARK: The host's side

    def _method_call(self, connection, sender, path, interface, method, parameters, invocation) -> None:
        if method in ("Activate", "SecondaryActivate", "ContextMenu"):
            if method != "ContextMenu":
                self._on_activate()
            invocation.return_value(None)
        elif method == "Scroll":
            invocation.return_value(None)
        else:
            invocation.return_dbus_error("org.freedesktop.DBus.Error.UnknownMethod", method)

    def _get_property(self, connection, sender, path, interface, name) -> GLib.Variant | None:
        match name:
            case "Category":
                return GLib.Variant("s", "ApplicationStatus")
            case "Id":
                return GLib.Variant("s", self._id)
            case "Title":
                return GLib.Variant("s", self._title)
            case "Status":
                return GLib.Variant("s", "Active")
            case "WindowId":
                return GLib.Variant("u", 0)
            case "IconThemePath":
                return GLib.Variant("s", self._icon_theme_path)
            case "Menu":
                return GLib.Variant("o", MENU_PATH)
            case "ItemIsMenu":
                return GLib.Variant("b", True)
            case "IconName" | "AttentionIconName":
                return GLib.Variant("s", self._icon)
            case "OverlayIconName" | "AttentionMovieName":
                return GLib.Variant("s", "")
            case "IconPixmap" | "OverlayIconPixmap" | "AttentionIconPixmap":
                return GLib.Variant("a(iiay)", [])
            case "ToolTip":
                return GLib.Variant("(sa(iiay)ss)", (self._icon, [], self._title, self._tooltip))
        return None
