"""The few rules of CSS the app adds to libadwaita's own."""

from __future__ import annotations

import gi

gi.require_version("Gtk", "4.0")
gi.require_version("Gdk", "4.0")
from gi.repository import Gdk, Gtk  # noqa: E402

from ..core.presentation import Tone  # noqa: E402

CSS = """
/* A state's glyph on a tile of its colour, centred against the words. */
.state-tile { border-radius: 13px; min-width: 52px; min-height: 52px; background-color: alpha(currentColor, .1); }
.state-tile.success { background-color: alpha(@success_color, .16); }
.state-tile.warning { background-color: alpha(@warning_color, .16); }
.state-tile.error { background-color: alpha(@error_color, .16); }

/* A glyph that moves while its state ends by itself. Animations are off when the person turned them off. */
.glyph-pulse { animation: glyph-pulse 1.4s ease-in-out infinite; }
@keyframes glyph-pulse { 0% { opacity: 1; } 50% { opacity: .4; } 100% { opacity: 1; } }

/* A note above a page, in the content layer: something the person may want to act on. */
.notice { border-radius: 10px; padding: 10px 12px; margin: 12px 12px 0 12px; background-color: alpha(currentColor, .08); }
.notice.warning { background-color: alpha(@warning_color, .16); }
.notice.error { background-color: alpha(@error_color, .16); }

/* What a page can do, in a strip at its top. */
.page-bar { padding: 6px 12px; border-bottom: 1px solid alpha(currentColor, .15); }

/* Where a dragged profile will land. */
row.drop-above { box-shadow: inset 0 2px 0 0 @accent_bg_color; }
row.drop-below { box-shadow: inset 0 -2px 0 0 @accent_bg_color; }
row.dragged { opacity: .5; }

/* Files dragged over the window. */
.drop-over { box-shadow: inset 0 0 0 3px @accent_bg_color; border-radius: 8px; }

/* libadwaita 1.7 styles .dimmed; Ubuntu 24.04 has 1.5, which does not. Same value as theirs. */
.dimmed { opacity: .55; }

.log-view, .config-editor { font-family: monospace; font-size: 90%; }
.log-view { padding: 8px 12px; }
.line-numbers { font-family: monospace; font-size: 80%; padding: 0 6px 0 10px; background-color: alpha(currentColor, .04); }

.rate { font-size: 130%; font-weight: 600; }
.mono { font-family: monospace; }
"""

TONE_CLASSES = {
    Tone.SUCCESS: "success",
    Tone.WARNING: "warning",
    Tone.ERROR: "error",
    Tone.MUTED: "dimmed",
    Tone.NORMAL: None,
}
_ALL_TONE_CLASSES = ("success", "warning", "error", "dimmed")


def install() -> None:
    provider = Gtk.CssProvider()
    provider.load_from_string(CSS)
    Gtk.StyleContext.add_provider_for_display(
        Gdk.Display.get_default(), provider, Gtk.STYLE_PROVIDER_PRIORITY_APPLICATION
    )


def set_tone(widget: Gtk.Widget, tone: Tone) -> None:
    for name in _ALL_TONE_CLASSES:
        widget.remove_css_class(name)
    name = TONE_CLASSES[tone]
    if name:
        widget.add_css_class(name)
