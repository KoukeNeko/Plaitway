"""The helper as a systemd unit, over the system bus.

Asking about the unit needs no permission. Starting and restarting it does:
the calls ask for interactive authorization, so polkit shows the person its
own dialog. Nothing here runs a command or needs sudo.
"""

from __future__ import annotations

import logging
import re
from pathlib import Path

import gi

from ..client.location import HELPER_UNIT
from ..core.ports import HelperRegistration, HelperServiceError

gi.require_version("Gio", "2.0")
from gi.repository import Gio, GLib  # noqa: E402

log = logging.getLogger(__name__)

BUS_NAME = "org.freedesktop.systemd1"
MANAGER_PATH = "/org/freedesktop/systemd1"
MANAGER = "org.freedesktop.systemd1.Manager"
UNIT = "org.freedesktop.systemd1.Unit"
PROPERTIES = "org.freedesktop.DBus.Properties"

# The directory systemd makes when it is the init of the system, which is what sd_booted() looks for.
SYSTEMD_RUN_DIR = Path("/run/systemd/system")
QUERY_TIMEOUT_MS = 5_000
# polkit waits for the person to type a password.
AUTHORIZATION_TIMEOUT_MS = 120_000
RUNNING_STATES = frozenset({"active", "activating", "reloading"})


def _plain(message: str) -> str:
    """The reason of a D-Bus error without its `GDBus.Error:org.freedesktop.DBus.Error.X: ` prefix."""
    return re.sub(r"^GDBus\.Error:[^:]*: ", "", message)


class SystemdHelperService:
    def __init__(self, unit: str = HELPER_UNIT, run_dir: Path = SYSTEMD_RUN_DIR) -> None:
        self._unit = unit
        self._run_dir = run_dir

    def _bus(self) -> Gio.DBusConnection:
        try:
            return Gio.bus_get_sync(Gio.BusType.SYSTEM, None)
        except GLib.Error as error:
            raise HelperServiceError(error.message) from error

    def _call(self, bus, path: str, interface: str, method: str, parameters, reply: str, flags, timeout: int):
        try:
            return bus.call_sync(
                BUS_NAME, path, interface, method, parameters, GLib.VariantType(reply), flags, timeout, None
            )
        except GLib.Error as error:
            raise HelperServiceError(_plain(error.message)) from error

    def registration(self) -> HelperRegistration:
        if not self._run_dir.is_dir():
            return HelperRegistration.NO_SYSTEMD
        try:
            bus = self._bus()
            (path,) = self._call(
                bus, MANAGER_PATH, MANAGER, "LoadUnit", GLib.Variant("(s)", (self._unit,)), "(o)",
                Gio.DBusCallFlags.NONE, QUERY_TIMEOUT_MS,
            )
            (properties,) = self._call(
                bus, path, PROPERTIES, "GetAll", GLib.Variant("(s)", (UNIT,)), "(a{sv})",
                Gio.DBusCallFlags.NONE, QUERY_TIMEOUT_MS,
            )
        except HelperServiceError as error:
            log.info("systemd cannot be asked about %s: %s", self._unit, error)
            return HelperRegistration.UNKNOWN
        if properties.get("LoadState") == "not-found":
            return HelperRegistration.NOT_INSTALLED
        if properties.get("ActiveState") in RUNNING_STATES:
            return HelperRegistration.RUNNING
        return HelperRegistration.STOPPED

    def start(self) -> None:
        self._job("StartUnit")

    def restart(self) -> None:
        self._job("RestartUnit")

    def _job(self, method: str) -> None:
        if not self._run_dir.is_dir():
            raise HelperServiceError("this system does not run systemd")
        bus = self._bus()
        self._call(
            bus, MANAGER_PATH, MANAGER, method, GLib.Variant("(ss)", (self._unit, "replace")), "(o)",
            Gio.DBusCallFlags.ALLOW_INTERACTIVE_AUTHORIZATION, AUTHORIZATION_TIMEOUT_MS,
        )
