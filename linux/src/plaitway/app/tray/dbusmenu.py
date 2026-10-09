"""com.canonical.dbusmenu: the menu of the tray item, as the desktop's tray host asks for it.

The menu is a flat list of entries under the root; each change of the model makes a
new layout with a new revision.
"""

from __future__ import annotations

import logging
from collections.abc import Callable
from dataclasses import dataclass

import gi

gi.require_version("Gio", "2.0")
from gi.repository import Gio, GLib  # noqa: E402

log = logging.getLogger(__name__)

INTERFACE = "com.canonical.dbusmenu"
OBJECT_PATH = "/MenuBar"

INTROSPECTION = """
<node>
  <interface name="com.canonical.dbusmenu">
    <property name="Version" type="u" access="read"/>
    <property name="TextDirection" type="s" access="read"/>
    <property name="Status" type="s" access="read"/>
    <property name="IconThemePath" type="as" access="read"/>
    <method name="GetLayout">
      <arg type="i" name="parentId" direction="in"/>
      <arg type="i" name="recursionDepth" direction="in"/>
      <arg type="as" name="propertyNames" direction="in"/>
      <arg type="u" name="revision" direction="out"/>
      <arg type="(ia{sv}av)" name="layout" direction="out"/>
    </method>
    <method name="GetGroupProperties">
      <arg type="ai" name="ids" direction="in"/>
      <arg type="as" name="propertyNames" direction="in"/>
      <arg type="a(ia{sv})" name="properties" direction="out"/>
    </method>
    <method name="GetProperty">
      <arg type="i" name="id" direction="in"/>
      <arg type="s" name="name" direction="in"/>
      <arg type="v" name="value" direction="out"/>
    </method>
    <method name="Event">
      <arg type="i" name="id" direction="in"/>
      <arg type="s" name="eventId" direction="in"/>
      <arg type="v" name="data" direction="in"/>
      <arg type="u" name="timestamp" direction="in"/>
    </method>
    <method name="EventGroup">
      <arg type="a(isvu)" name="events" direction="in"/>
      <arg type="ai" name="idErrors" direction="out"/>
    </method>
    <method name="AboutToShow">
      <arg type="i" name="id" direction="in"/>
      <arg type="b" name="needUpdate" direction="out"/>
    </method>
    <method name="AboutToShowGroup">
      <arg type="ai" name="ids" direction="in"/>
      <arg type="ai" name="updatesNeeded" direction="out"/>
      <arg type="ai" name="idErrors" direction="out"/>
    </method>
    <signal name="ItemsPropertiesUpdated">
      <arg type="a(ia{sv})" name="updatedProps"/>
      <arg type="a(ias)" name="removedProps"/>
    </signal>
    <signal name="LayoutUpdated">
      <arg type="u" name="revision"/>
      <arg type="i" name="parent"/>
    </signal>
    <signal name="ItemActivationRequested">
      <arg type="i" name="id"/>
      <arg type="u" name="timestamp"/>
    </signal>
  </interface>
</node>
"""


@dataclass(frozen=True)
class MenuEntry:
    """One row of the menu. A separator has no label."""

    label: str = ""
    enabled: bool = True
    separator: bool = False
    # None: no check mark; 1 checked, 0 not, -1 in between.
    check: int | None = None
    on_activate: Callable[[], None] | None = None

    @staticmethod
    def divider() -> MenuEntry:
        return MenuEntry(separator=True)


class DBusMenu:
    def __init__(self) -> None:
        self._entries: list[MenuEntry] = []
        self._revision = 1
        self._connection: Gio.DBusConnection | None = None
        self._registration = 0
        self._info = Gio.DBusNodeInfo.new_for_xml(INTROSPECTION).interfaces[0]

    # MARK: Registration

    def export(self, connection: Gio.DBusConnection, icon_theme_path: str = "") -> None:
        self._connection = connection
        self._icon_theme_path = icon_theme_path
        self._registration = connection.register_object(
            OBJECT_PATH, self._info, self._method_call, self._get_property, None
        )

    def unexport(self) -> None:
        if self._connection is not None and self._registration:
            self._connection.unregister_object(self._registration)
        self._registration = 0
        self._connection = None

    # MARK: The model's side

    def set_entries(self, entries: list[MenuEntry]) -> None:
        """Replaces the menu. The host asks for the layout again when it is told it changed."""
        if entries == self._entries:
            return
        self._entries = entries
        self._revision += 1
        self._emit("LayoutUpdated", GLib.Variant("(ui)", (self._revision, 0)))

    @property
    def entries(self) -> list[MenuEntry]:
        return list(self._entries)

    def _emit(self, signal: str, parameters: GLib.Variant) -> None:
        if self._connection is not None:
            self._connection.emit_signal(None, OBJECT_PATH, INTERFACE, signal, parameters)

    # MARK: The host's side

    def _properties(self, index: int, entry: MenuEntry) -> dict[str, GLib.Variant]:
        if entry.separator:
            return {"type": GLib.Variant("s", "separator")}
        properties = {
            "label": GLib.Variant("s", entry.label),
            "enabled": GLib.Variant("b", entry.enabled),
            "visible": GLib.Variant("b", True),
        }
        if entry.check is not None:
            properties["toggle-type"] = GLib.Variant("s", "checkmark")
            properties["toggle-state"] = GLib.Variant("i", entry.check)
        return properties

    def _layout(self) -> tuple:
        """The root and its entries, as the (ia{sv}av) the host reads: each child is a variant."""
        children = [
            GLib.Variant("(ia{sv}av)", (index + 1, self._properties(index, entry), []))
            for index, entry in enumerate(self._entries)
        ]
        root = {"children-display": GLib.Variant("s", "submenu")}
        return 0, root, children

    def _entry(self, item_id: int) -> MenuEntry | None:
        return self._entries[item_id - 1] if 1 <= item_id <= len(self._entries) else None

    def _filtered(self, properties: dict[str, GLib.Variant], names: list[str]) -> dict[str, GLib.Variant]:
        return {k: v for k, v in properties.items() if not names or k in names}

    def _method_call(self, connection, sender, path, interface, method, parameters, invocation) -> None:
        try:
            result = self._dispatch(method, parameters)
        except Exception as error:
            log.exception("the dbusmenu call %s failed", method)
            invocation.return_dbus_error("org.freedesktop.DBus.Error.Failed", str(error))
            return
        invocation.return_value(result)

    def _dispatch(self, method: str, parameters: GLib.Variant) -> GLib.Variant | None:
        args = parameters.unpack()
        match method:
            case "GetLayout":
                return GLib.Variant("(u(ia{sv}av))", (self._revision, self._layout()))
            case "GetGroupProperties":
                ids, names = args
                found = []
                for item_id in ids or range(len(self._entries) + 1):
                    if item_id == 0:
                        found.append((0, {}))
                    elif (entry := self._entry(item_id)) is not None:
                        found.append((item_id, self._filtered(self._properties(item_id - 1, entry), names)))
                return GLib.Variant("(a(ia{sv}))", (found,))
            case "GetProperty":
                item_id, name = args
                entry = self._entry(item_id)
                value = self._properties(item_id - 1, entry).get(name) if entry else None
                return GLib.Variant("(v)", (value if value is not None else GLib.Variant("s", ""),))
            case "Event":
                item_id, event, _, _ = args
                self._activated(item_id, event)
                return None
            case "EventGroup":
                (events,) = args
                errors = []
                for item_id, event, _, _ in events:
                    if self._entry(item_id) is None:
                        errors.append(item_id)
                    else:
                        self._activated(item_id, event)
                return GLib.Variant("(ai)", (errors,))
            case "AboutToShow":
                return GLib.Variant("(b)", (False,))
            case "AboutToShowGroup":
                return GLib.Variant("(aiai)", ([], []))
        raise ValueError(f"unknown method {method}")

    def _activated(self, item_id: int, event: str) -> None:
        entry = self._entry(item_id)
        if event == "clicked" and entry is not None and entry.enabled and entry.on_activate is not None:
            entry.on_activate()

    def _get_property(self, connection, sender, path, interface, name) -> GLib.Variant | None:
        match name:
            case "Version":
                return GLib.Variant("u", 3)
            case "TextDirection":
                return GLib.Variant("s", "ltr")
            case "Status":
                return GLib.Variant("s", "normal")
            case "IconThemePath":
                return GLib.Variant("as", [self._icon_theme_path] if self._icon_theme_path else [])
        return None
