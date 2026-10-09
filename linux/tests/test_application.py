"""The application as a process: one instance per user, files opened through it, what closing
the window does with and without a tray, and what is remembered."""

import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path

import gi

gi.require_version("Gio", "2.0")
from gi.repository import Gio, GLib  # noqa: E402

from support import DaemonTestCase  # noqa: E402
from test_tray import PrivateBus, pump  # noqa: E402

LINUX_DIR = Path(__file__).resolve().parents[1]
LAUNCHER = LINUX_DIR / "bin" / "plaitway-app"
APP_ID = "io.github.koukeneko.Plaitway"


class ApplicationTestCase(DaemonTestCase):
    def setUp(self):
        super().setUp()
        if not os.environ.get("WAYLAND_DISPLAY") and not os.environ.get("DISPLAY"):
            self.skipTest("there is no display to open a window on")
        self.bus = PrivateBus()
        self.addCleanup(self.bus.close)
        self.home = tempfile.mkdtemp(prefix="pw-home-")
        self.addCleanup(shutil.rmtree, self.home, True)
        self.processes: list[subprocess.Popen] = []
        self.addCleanup(self.stop_processes)
        self.environment = {
            **os.environ,
            "DBUS_SESSION_BUS_ADDRESS": self.bus.address,
            "PLAITWAY_DEV": "1",
            "PLAITWAY_SOCKET": self.daemon.socket_path,
            "PLAITWAY_LANGUAGE": "en",
            "XDG_STATE_HOME": os.path.join(self.home, "state"),
            "XDG_CONFIG_HOME": os.path.join(self.home, "config"),
            "PYTHONPATH": f"{LINUX_DIR / 'src'}:{LINUX_DIR}",
        }

    def stop_processes(self):
        for process in self.processes:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(5)
                except subprocess.TimeoutExpired:
                    process.kill()

    def start(self, *arguments, module: str | None = None, **kwargs) -> subprocess.Popen:
        command = [sys.executable, "-m", module, *arguments] if module else [sys.executable, str(LAUNCHER), *arguments]
        process = subprocess.Popen(command, env=self.environment, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, **kwargs)
        self.processes.append(process)
        return process

    def wait_for_owner(self, name: str, timeout: float = 15.0):
        connection = self.bus.connect()
        self.addCleanup(connection.close_sync, None)
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            result = connection.call_sync(
                "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "NameHasOwner",
                GLib.Variant("(s)", (name,)), GLib.VariantType("(b)"), Gio.DBusCallFlags.NONE, 2000, None,
            )
            if result.unpack()[0]:
                return connection
            time.sleep(0.05)
        self.fail(f"nobody owns {name}")

    def activate_action(self, connection, name: str) -> None:
        connection.call_sync(
            APP_ID, "/io/github/koukeneko/Plaitway", "org.gtk.Actions", "Activate",
            GLib.Variant("(sava{sv})", (name, [], {})), None, Gio.DBusCallFlags.NONE, 2000, None,
        )


class InstanceTests(ApplicationTestCase):
    def test_a_second_start_hands_its_files_to_the_first_and_quit_ends_it(self):
        primary = self.start()
        connection = self.wait_for_owner(APP_ID)
        profile = Path(self.home) / "office.ovpn"
        profile.write_text("client\nremote vpn.example.net 1194\n")

        second = self.start(str(profile))
        self.assertEqual(second.wait(15), 0, second.stderr.read())
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline and not self.client.list_profiles():
            time.sleep(0.1)
        self.assertEqual([p.name for p in self.client.list_profiles()], ["office"])

        # Starting it again raises the window of the instance that runs and leaves no second one.
        third = self.start()
        self.assertEqual(third.wait(15), 0, third.stderr.read())
        self.assertIsNone(primary.poll())

        self.activate_action(connection, "quit")
        self.assertEqual(primary.wait(15), 0, primary.stderr.read())

    def test_the_size_of_the_window_is_remembered(self):
        primary = self.start()
        connection = self.wait_for_owner(APP_ID)
        time.sleep(1.5)
        self.activate_action(connection, "quit")
        primary.wait(15)
        state = json.loads((Path(self.home) / "state" / "plaitway" / "state.json").read_text())
        self.assertEqual(set(state["window"]), {"width", "height", "maximized"})
        self.assertGreater(state["window"]["width"], 300)

    def test_a_start_at_login_shows_no_window_but_opens_one_when_there_is_no_tray(self):
        # --background waits a moment for a tray; without one the app would be out of reach.
        primary = self.start("--background")
        connection = self.wait_for_owner(APP_ID)
        time.sleep(3)
        self.assertIsNone(primary.poll(), "the app ended instead of showing a window")
        self.activate_action(connection, "quit")
        self.assertEqual(primary.wait(15), 0)

    def test_prints_its_version(self):
        process = self.start("--version")
        output, _ = process.communicate(timeout=15)
        self.assertRegex(output, r"^plaitway-app \S+")


class CloseTests(ApplicationTestCase):
    def run_scenario(self, name: str) -> str:
        process = self.start(name, module="tests.application_scenario")
        try:
            output, errors = process.communicate(timeout=30)
        except subprocess.TimeoutExpired:
            process.kill()
            self.fail("the scenario did not end")
        return output.strip() + errors.strip()

    def test_closing_the_window_quits_the_app_when_nothing_shows_it_in_a_tray(self):
        self.assertTrue(self.run_scenario("close-quits").startswith("OK"))

    def test_closing_the_window_hides_it_when_a_tray_shows_the_app(self):
        self.bus.start_watcher()
        # The scenario runs in a process of its own; the watcher runs here, so this process serves the bus.
        process = self.start("close-hides", module="tests.application_scenario")
        deadline = time.monotonic() + 30
        while process.poll() is None and time.monotonic() < deadline:
            pump(0.05)
        self.assertEqual(process.poll(), 0, "the scenario did not end")
        output = process.stdout.read()
        self.assertIn("OK", output, process.stderr.read())


if __name__ == "__main__":
    unittest.main()
