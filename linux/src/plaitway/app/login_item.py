"""Starting the app when the person logs in: an autostart entry of the desktop session."""

from __future__ import annotations

import os
from pathlib import Path

from .paths import APP_ID

ENTRY = """[Desktop Entry]
Type=Application
Name=Plaitway
Exec=plaitway-app --background
Icon={app_id}
Terminal=false
NoDisplay=true
X-GNOME-Autostart-enabled=true
"""


class AutostartLoginItem:
    def __init__(self, config_home: Path | None = None) -> None:
        base = config_home or Path(os.environ.get("XDG_CONFIG_HOME") or Path.home() / ".config")
        self._path = base / "autostart" / f"{APP_ID}.desktop"

    @property
    def is_enabled(self) -> bool:
        return self._path.is_file()

    def set_enabled(self, enabled: bool) -> None:
        if enabled:
            self._path.parent.mkdir(parents=True, exist_ok=True)
            self._path.write_text(ENTRY.format(app_id=APP_ID), encoding="utf-8")
        else:
            self._path.unlink(missing_ok=True)
