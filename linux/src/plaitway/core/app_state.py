"""What the app remembers between runs: the size of the window and the one
setting that is the app's own. A small JSON file in $XDG_STATE_HOME/plaitway."""

from __future__ import annotations

import json
import logging
import os
import threading
from dataclasses import dataclass
from pathlib import Path

log = logging.getLogger(__name__)

DEFAULT_WIDTH = 1040
DEFAULT_HEIGHT = 660


def default_path(environment: dict[str, str] | None = None) -> Path:
    environment = dict(os.environ) if environment is None else environment
    base = environment.get("XDG_STATE_HOME") or str(Path.home() / ".local" / "state")
    return Path(base) / "plaitway" / "state.json"


@dataclass(frozen=True)
class WindowGeometry:
    width: int = DEFAULT_WIDTH
    height: int = DEFAULT_HEIGHT
    maximized: bool = False


class AppState:
    """Reads the file once and writes it on every change. A file that is missing or
    does not make sense gives the defaults; one that cannot be written is logged."""

    def __init__(self, path: Path | None = None) -> None:
        self._path = path or default_path()
        self._lock = threading.Lock()
        self._data = self._read()

    def _read(self) -> dict:
        try:
            data = json.loads(self._path.read_text(encoding="utf-8"))
        except FileNotFoundError:
            return {}
        except (OSError, ValueError) as error:
            log.warning("ignoring %s: %s", self._path, error)
            return {}
        return data if isinstance(data, dict) else {}

    def _write(self) -> None:
        try:
            self._path.parent.mkdir(parents=True, exist_ok=True)
            temporary = self._path.with_suffix(".tmp")
            temporary.write_text(json.dumps(self._data, indent=2) + "\n", encoding="utf-8")
            os.replace(temporary, self._path)
        except OSError as error:
            log.warning("could not write %s: %s", self._path, error)

    @property
    def window(self) -> WindowGeometry:
        with self._lock:
            stored = self._data.get("window")
        if not isinstance(stored, dict):
            return WindowGeometry()
        width, height = stored.get("width"), stored.get("height")
        if not (isinstance(width, int) and isinstance(height, int) and width > 0 and height > 0):
            return WindowGeometry()
        return WindowGeometry(width, height, stored.get("maximized") is True)

    def set_window(self, geometry: WindowGeometry) -> None:
        with self._lock:
            self._data["window"] = {
                "width": geometry.width, "height": geometry.height, "maximized": geometry.maximized,
            }
            self._write()

    @property
    def show_tray_icon(self) -> bool:
        with self._lock:
            return self._data.get("show_tray_icon", True) is not False

    def set_show_tray_icon(self, shown: bool) -> None:
        with self._lock:
            self._data["show_tray_icon"] = shown
            self._write()
