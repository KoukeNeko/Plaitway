"""The extension on this machine: where it is installed, whether it is enabled, and the changes the Tools page
makes. Everything here blocks (files, the Shell's settings and D-Bus): not for the UI thread."""

from __future__ import annotations

import logging
import shutil
from pathlib import Path

import gi

gi.require_version("Gio", "2.0")
from gi.repository import Gio, GLib  # noqa: E402

from ..core.gnome_extension import UUID, ExtensionStatus  # noqa: E402
from . import paths  # noqa: E402

log = logging.getLogger(__name__)

SHELL_SCHEMA = "org.gnome.shell"
ENABLED_KEY = "enabled-extensions"
DISABLED_KEY = "disabled-extensions"
USER_DIR = Path(GLib.get_user_data_dir()) / "gnome-shell" / "extensions" / UUID
SYSTEM_DIRS = tuple(Path(d) / "gnome-shell" / "extensions" / UUID for d in GLib.get_system_data_dirs())
# The Shell tells the state of an extension as a number; 1 is active.
STATE_ACTIVE = 1


def bundled() -> Path:
    """The extension that ships with the app: in the checkout, or in /usr/share."""
    return paths.data_dir() / "gnome-shell" / "extensions" / UUID


def _shell_settings() -> Gio.Settings | None:
    source = Gio.SettingsSchemaSource.get_default()
    if source is None or source.lookup(SHELL_SCHEMA, True) is None:
        return None
    return Gio.Settings.new(SHELL_SCHEMA)


def _loaded() -> bool | None:
    """Whether the running Shell has loaded the extension; None where it cannot be asked."""
    try:
        connection = Gio.bus_get_sync(Gio.BusType.SESSION, None)
        answer = connection.call_sync(
            "org.gnome.Shell.Extensions", "/org/gnome/Shell/Extensions", "org.gnome.Shell.Extensions", "ListExtensions",
            None, GLib.VariantType("(a{sa{sv}})"), Gio.DBusCallFlags.NONE, 3000, None,
        )
    except GLib.Error as error:
        log.debug("the Shell did not list its extensions: %s", error.message)
        return None
    extensions = answer.unpack()[0]
    return UUID in extensions and extensions[UUID].get("state") == STATE_ACTIVE


def inspect() -> ExtensionStatus:
    settings = _shell_settings()
    if settings is None:
        return ExtensionStatus.of(shell=False, installed=False, user_installed=False, enabled=False, loaded=None)
    user_installed = (USER_DIR / "metadata.json").is_file()
    installed = user_installed or any((directory / "metadata.json").is_file() for directory in SYSTEM_DIRS)
    enabled = UUID in settings.get_strv(ENABLED_KEY)
    return ExtensionStatus.of(
        shell=True, installed=installed, user_installed=user_installed, enabled=enabled, loaded=_loaded() if enabled else None
    )


def install() -> None:
    """Copies the extension into the user's folder and enables it."""
    source = bundled()
    if not (source / "metadata.json").is_file():
        raise FileNotFoundError(f"{source} has no extension")
    USER_DIR.parent.mkdir(parents=True, exist_ok=True)
    shutil.copytree(source, USER_DIR, dirs_exist_ok=True)
    set_enabled(True)


def uninstall() -> None:
    """Forgets the copy in the user's folder and removes it. A copy a package installed is not the app's to remove."""
    set_enabled(None)
    shutil.rmtree(USER_DIR, ignore_errors=False)


def set_enabled(enabled: bool | None) -> None:
    """On or off; None takes it out of both lists."""
    settings = _shell_settings()
    if settings is None:
        raise RuntimeError("there is no GNOME Shell")
    on = [name for name in settings.get_strv(ENABLED_KEY) if name != UUID]
    off = [name for name in settings.get_strv(DISABLED_KEY) if name != UUID]
    if enabled is True:
        on.append(UUID)
    elif enabled is False:
        off.append(UUID)
    settings.set_strv(ENABLED_KEY, on)
    settings.set_strv(DISABLED_KEY, off)
    settings.sync()
