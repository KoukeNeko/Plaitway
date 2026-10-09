"""The tray item against a private session bus with a StatusNotifierWatcher of its own."""

import os
import shutil
import subprocess
import tempfile
import time
import unittest

import gi

gi.require_version("Gio", "2.0")
from gi.repository import Gio, GLib  # noqa: E402

from plaitway.app.tray.controller import TrayCommands, TrayController  # noqa: E402
from plaitway.app.tray.dbusmenu import DBusMenu, MenuEntry  # noqa: E402
from plaitway.client.types import ProfileState  # noqa: E402

from support import AppTestCase, openvpn_profile  # noqa: E402

WATCHER_XML = """
<node><interface name="org.kde.StatusNotifierWatcher">
  <method name="RegisterStatusNotifierItem"><arg type="s" name="service" direction="in"/></method>
  <property name="IsStatusNotifierHostRegistered" type="b" access="read"/>
  <property name="RegisteredStatusNotifierItems" type="as" access="read"/>
</interface></node>
"""


BUS_CONFIG = """<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:tmpdir=/tmp</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
"""


class PrivateBus:
    """A session bus of its own, with a StatusNotifierWatcher that can come and go."""

    def __init__(self) -> None:
        if shutil.which("dbus-daemon") is None:
            raise unittest.SkipTest("there is no dbus-daemon to run a private bus")
        # No service files: nothing the desktop would start (portals, a keyring) starts on this bus.
        self._config = tempfile.NamedTemporaryFile("w", suffix=".conf", prefix="pw-bus-", delete=False)
        self._config.write(BUS_CONFIG)
        self._config.close()
        self._daemon = subprocess.Popen(
            ["dbus-daemon", f"--config-file={self._config.name}", "--nofork", "--print-address=1"],
            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True,
        )
        self.address = self._daemon.stdout.readline().strip()
        self.registered: list[str] = []
        self._watcher_connection: Gio.DBusConnection | None = None
        self._name = 0

    def connect(self) -> Gio.DBusConnection:
        return Gio.DBusConnection.new_for_address_sync(
            self.address,
            Gio.DBusConnectionFlags.AUTHENTICATION_CLIENT | Gio.DBusConnectionFlags.MESSAGE_BUS_CONNECTION,
            None,
            None,
        )

    def start_watcher(self) -> None:
        connection = self.connect()
        info = Gio.DBusNodeInfo.new_for_xml(WATCHER_XML).interfaces[0]

        def method_call(conn, sender, path, interface, method, parameters, invocation):
            self.registered.append(parameters.unpack()[0])
            invocation.return_value(None)

        def get_property(conn, sender, path, interface, name):
            if name == "IsStatusNotifierHostRegistered":
                return GLib.Variant("b", True)
            return GLib.Variant("as", self.registered)

        connection.register_object("/StatusNotifierWatcher", info, method_call, get_property, None)
        self._watcher_connection = connection
        self._name = Gio.bus_own_name_on_connection(
            connection, "org.kde.StatusNotifierWatcher", Gio.BusNameOwnerFlags.NONE, None, None
        )

    def stop_watcher(self) -> None:
        if self._name:
            Gio.bus_unown_name(self._name)
            self._name = 0
        if self._watcher_connection is not None:
            self._watcher_connection.close_sync(None)
            self._watcher_connection = None

    def close(self) -> None:
        self.stop_watcher()
        self._daemon.terminate()
        self._daemon.wait()
        os.unlink(self._config.name)


def pump(seconds: float = 0.3, until=None) -> None:
    context = GLib.MainContext.default()
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        while context.iteration(False):
            pass
        if until is not None and until():
            return
        time.sleep(0.005)


class DBusMenuTests(unittest.TestCase):
    """The menu on a private bus, asked for as a tray host asks."""

    def setUp(self):
        self.bus = PrivateBus()
        self.addCleanup(self.bus.close)
        self.connection = self.bus.connect()
        self.addCleanup(self.connection.close_sync, None)
        self.menu = DBusMenu()
        self.menu.export(self.connection)
        self.addCleanup(self.menu.unexport)
        self.host = self.bus.connect()
        self.addCleanup(self.host.close_sync, None)

    def asked(self, method, parameters, reply):
        """Calls from the host while this thread serves the bus."""
        outcome = []
        Gio.DBusConnection.call(
            self.host, self.connection.get_unique_name(), "/MenuBar", "com.canonical.dbusmenu", method, parameters,
            GLib.VariantType(reply), Gio.DBusCallFlags.NONE, 2000, None,
            lambda conn, result: outcome.append(conn.call_finish(result).unpack()),
        )
        pump(2, lambda: outcome)
        return outcome[0]

    def test_the_layout_is_a_root_with_a_row_for_each_entry(self):
        clicked = []
        self.menu.set_entries([
            MenuEntry("Connected: 1", enabled=False),
            MenuEntry("Office — OpenVPN · Connected", check=1, on_activate=lambda: clicked.append("office")),
            MenuEntry.divider(),
            MenuEntry("Quit Plaitway", on_activate=lambda: clicked.append("quit")),
        ])
        revision, layout = self.asked("GetLayout", GLib.Variant("(iias)", (0, -1, [])), "(u(ia{sv}av))")
        root_id, root_properties, children = layout
        self.assertEqual(root_id, 0)
        self.assertEqual(root_properties["children-display"], "submenu")
        rows = [(child[0], {k: v for k, v in child[1].items()}) for child in children]
        self.assertEqual([row[0] for row in rows], [1, 2, 3, 4])
        self.assertEqual(rows[0][1], {"label": "Connected: 1", "enabled": False, "visible": True})
        self.assertEqual(rows[1][1]["toggle-type"], "checkmark")
        self.assertEqual(rows[1][1]["toggle-state"], 1)
        self.assertEqual(rows[2][1], {"type": "separator"})

        self.asked("Event", GLib.Variant("(isvu)", (2, "clicked", GLib.Variant("s", ""), 0)), "()")
        self.asked("Event", GLib.Variant("(isvu)", (1, "clicked", GLib.Variant("s", ""), 0)), "()")  # disabled
        self.asked("Event", GLib.Variant("(isvu)", (4, "clicked", GLib.Variant("s", ""), 0)), "()")
        self.assertEqual(clicked, ["office", "quit"])

    def test_a_change_makes_a_new_revision_and_is_announced(self):
        heard = []
        self.host.signal_subscribe(
            None, "com.canonical.dbusmenu", "LayoutUpdated", "/MenuBar", None, Gio.DBusSignalFlags.NONE,
            lambda *args: heard.append(args[5].unpack()),
        )
        self.menu.set_entries([MenuEntry("One")])
        pump(1, lambda: heard)
        first = heard[0][0]
        self.menu.set_entries([MenuEntry("One")])  # the same: nothing to announce
        self.menu.set_entries([MenuEntry("Two")])
        pump(1, lambda: len(heard) > 1)
        self.assertEqual(len(heard), 2)
        self.assertEqual(heard[1][0], first + 1)

    def test_answers_the_other_calls_of_the_interface(self):
        self.menu.set_entries([MenuEntry("One", check=0), MenuEntry("Two")])
        properties = self.asked("GetGroupProperties", GLib.Variant("(aias)", ([1, 2], ["label"])), "(a(ia{sv}))")[0]
        self.assertEqual(properties, [(1, {"label": "One"}), (2, {"label": "Two"})])
        self.assertEqual(self.asked("GetProperty", GLib.Variant("(is)", (1, "toggle-state")), "(v)")[0], 0)
        self.assertEqual(self.asked("AboutToShow", GLib.Variant("(i)", (0,)), "(b)")[0], False)


class TrayTests(AppTestCase):
    def setUp(self):
        super().setUp()
        self.bus = PrivateBus()
        self.addCleanup(self.bus.close)
        self.host = self.bus.connect()
        self.addCleanup(self.host.close_sync, None)

    def make_tray(self, app):
        self.commands = []
        self.availability = []
        commands = TrayCommands(
            open_window=lambda: self.commands.append("open"),
            import_profile=lambda: self.commands.append("import"),
            open_settings=lambda: self.commands.append("settings"),
            disconnect_all=lambda: self.commands.append("disconnect-all"),
            quit=lambda: self.commands.append("quit"),
        )
        tray = TrayController(app.model, commands, "", self.availability.append, connection=self.bus.connect())
        self.addCleanup(tray.stop)
        return tray

    def property(self, tray, name):
        """A property of the item as the host reads it: through the bus, while this thread serves it."""
        outcome = []
        self.host.call(
            tray._item.bus_name, "/StatusNotifierItem", "org.freedesktop.DBus.Properties", "Get",
            GLib.Variant("(ss)", ("org.kde.StatusNotifierItem", name)), GLib.VariantType("(v)"), Gio.DBusCallFlags.NONE, 2000, None,
            lambda connection, result: outcome.append(connection.call_finish(result).unpack()[0]),
        )
        pump(2, lambda: outcome)
        return outcome[0]

    def pump_model(self, app, seconds=0.4):
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            app.scheduler.run_pending()
            pump(0.02)

    def test_there_is_no_tray_without_a_watcher_and_one_when_it_appears(self):
        app = self.make_app()
        tray = self.make_tray(app)
        tray.start()
        tray.set_wanted(True)
        pump(0.3)
        self.assertFalse(tray.available)

        self.bus.start_watcher()
        pump(2, lambda: tray.available)
        self.assertTrue(tray.available)
        self.assertEqual(self.availability, [True])
        self.assertEqual(self.bus.registered, [tray._item.bus_name])

        self.bus.stop_watcher()
        pump(2, lambda: not tray.available)
        self.assertEqual(self.availability, [True, False])

    def test_the_item_shows_the_state_in_its_icon_and_the_rows_in_its_menu(self):
        app = self.make_app()
        office = self.import_fixture(app, "office.ovpn", openvpn_profile(host="vpn.example.net"))
        tray = self.make_tray(app)
        self.bus.start_watcher()
        tray.start()
        tray.set_wanted(True)
        pump(2, lambda: tray.available)
        self.assertTrue(tray.available)
        self.pump_model(app)

        self.assertEqual(self.property(tray, "IconName"), "plaitway-state-idle-symbolic")
        self.assertEqual(self.property(tray, "Menu"), "/MenuBar")
        self.assertEqual(self.property(tray, "Category"), "ApplicationStatus")
        labels = [entry.label for entry in tray.menu.entries]
        self.assertEqual(labels, [
            "Not connected", "office — OpenVPN · Disconnected", "", "Open Plaitway", "Import Profile…", "Settings…", "", "Quit Plaitway",
        ])
        self.assertEqual(self.property(tray, "ToolTip")[3], "Not connected")

        app.store.set_enabled(True, office)
        self.wait_for_state(app, office, ProfileState.CONNECTED)
        self.pump_model(app, 0.6)
        self.assertEqual(self.property(tray, "IconName"), "plaitway-state-connected-symbolic")
        labels = [entry.label for entry in tray.menu.entries]
        self.assertEqual(labels[:2], ["Connected: 1", "office — OpenVPN · Connected"])
        self.assertIn("Disconnect All", labels)
        self.assertEqual(tray.menu.entries[1].check, 1)

    def test_choosing_a_row_does_what_the_profile_needs(self):
        app = self.make_app()
        office = self.import_fixture(app, "office.ovpn")
        tray = self.make_tray(app)
        self.bus.start_watcher()
        tray.start()
        tray.set_wanted(True)
        pump(2, lambda: tray.available)
        self.pump_model(app)

        tray.menu.entries[1].on_activate()  # office, which is off
        self.wait_for_state(app, office, ProfileState.CONNECTED)
        self.pump_model(app, 0.4)
        tray.menu.entries[1].on_activate()  # now on: switch it off
        self.wait_for_state(app, office, ProfileState.DISCONNECTED)

        by_label = {entry.label: entry for entry in tray.menu.entries}
        by_label["Open Plaitway"].on_activate()
        by_label["Import Profile…"].on_activate()
        by_label["Settings…"].on_activate()
        by_label["Quit Plaitway"].on_activate()
        self.assertEqual(self.commands, ["open", "import", "settings", "quit"])

    def test_the_setting_hides_the_item(self):
        app = self.make_app()
        tray = self.make_tray(app)
        self.bus.start_watcher()
        tray.start()
        tray.set_wanted(True)
        pump(2, lambda: tray.available)
        tray.set_wanted(False)
        self.assertFalse(tray.available)
        self.assertFalse(tray._item.registered)
        tray.set_wanted(True)
        pump(2, lambda: tray.available)
        self.assertTrue(tray.available)


if __name__ == "__main__":
    unittest.main()
