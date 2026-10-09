#!/usr/bin/env python3
"""Makes the PNG set of the app icon from Docs/app-icon.png, with GdkPixbuf.

Writes data/share/icons/hicolor/<size>x<size>/apps/io.github.koukeneko.Plaitway.png.
The symbolic icons beside them (hicolor/symbolic/apps/plaitway-state-*-symbolic.svg)
are drawn by hand and are not made here.

  python tools/make_icons.py
"""

from pathlib import Path

import gi

gi.require_version("GdkPixbuf", "2.0")
from gi.repository import GdkPixbuf  # noqa: E402

LINUX_DIR = Path(__file__).resolve().parent.parent
SOURCE = LINUX_DIR.parent / "Docs" / "app-icon.png"
ICONS = LINUX_DIR / "data" / "share" / "icons" / "hicolor"
APP_ID = "io.github.koukeneko.Plaitway"
SIZES = (16, 32, 48, 64, 128, 256, 512)


def main() -> None:
    source = GdkPixbuf.Pixbuf.new_from_file(str(SOURCE))
    if source.get_width() != source.get_height():
        raise SystemExit(f"{SOURCE} is not square")
    for size in SIZES:
        directory = ICONS / f"{size}x{size}" / "apps"
        directory.mkdir(parents=True, exist_ok=True)
        icon = source if size == source.get_width() else source.scale_simple(size, size, GdkPixbuf.InterpType.HYPER)
        destination = directory / f"{APP_ID}.png"
        icon.savev(str(destination), "png", ["compression"], ["9"])
        print(f"wrote {destination.relative_to(LINUX_DIR.parent)}")


if __name__ == "__main__":
    main()
