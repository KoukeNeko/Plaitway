"""Colours for text that tags carry: a GtkTextTag cannot name a colour of the style
sheet, so the palette follows the light and dark style and the high contrast setting."""

from __future__ import annotations

import gi

gi.require_version("Gtk", "4.0")
gi.require_version("Adw", "1")
gi.require_version("Gdk", "4.0")
from gi.repository import Adw, Gdk, Gtk  # noqa: E402


def rgba(text: str) -> Gdk.RGBA:
    color = Gdk.RGBA()
    color.parse(text)
    return color


class Palette:
    """(light, dark, light high contrast, dark high contrast)"""

    def __init__(self, light: str, dark: str, light_contrast: str, dark_contrast: str) -> None:
        self._colours = (rgba(light), rgba(dark), rgba(light_contrast), rgba(dark_contrast))

    def current(self) -> Gdk.RGBA:
        manager = Adw.StyleManager.get_default()
        index = (1 if manager.get_dark() else 0) + (2 if manager.get_high_contrast() else 0)
        return self._colours[index]


WARNING = Palette("#9c6e03", "#f8e45c", "#6b4a00", "#ffff80")
ERROR = Palette("#c01c28", "#ff7b63", "#8a0f19", "#ffb3a3")


class TextPalette:
    """Keeps the colours of a text view's tags right while the style changes.

    `tags` maps a tone, or "time", to its tag.
    """

    def __init__(self, view: Gtk.TextView, tags: dict) -> None:
        self._view = view
        self._tags = tags
        manager = Adw.StyleManager.get_default()
        manager.connect("notify::dark", lambda *_: self.apply())
        manager.connect("notify::high-contrast", lambda *_: self.apply())
        view.connect("realize", lambda *_: self.apply())
        view.connect("notify::root", lambda *_: self.apply())

    def apply(self) -> None:
        from ..core.presentation import Tone

        foreground = self._view.get_color()
        dim = foreground.copy()
        dim.alpha = 0.6
        for key, colour in [
            (Tone.WARNING, WARNING.current()),
            (Tone.ERROR, ERROR.current()),
            (Tone.MUTED, dim),
            ("time", dim),
        ]:
            self._tags[key].set_property("foreground-rgba", colour)
