"""Where the data files are: next to the source tree when it runs from a checkout,
under /usr/share when it is installed."""

from __future__ import annotations

from pathlib import Path

# The product name is not translated.
APP_NAME = "Plaitway"
APP_ID = "io.github.koukeneko.Plaitway"

_MARKER = Path("icons/hicolor/symbolic/apps/plaitway-state-idle-symbolic.svg")


def data_dir() -> Path:
    """The `share` directory with the icons: linux/data/share in a checkout, else /usr/share."""
    checkout = Path(__file__).resolve().parents[3] / "data" / "share"
    for candidate in (checkout, Path("/usr/local/share"), Path("/usr/share")):
        if (candidate / _MARKER).is_file():
            return candidate
    return checkout


def from_checkout() -> bool:
    """Running from the source tree: its icons are not in the icon theme's search path."""
    return (Path(__file__).resolve().parents[3] / "data" / "share" / _MARKER).is_file()
