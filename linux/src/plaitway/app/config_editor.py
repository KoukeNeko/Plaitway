"""A plain text editor for a profile: monospaced, with line numbers, the directives
and values in colour, the placeholders of hidden secrets set apart, and the line the
daemon refused marked.

GtkTextView has no line numbers, and GtkSourceView is not a dependency of the app,
so the numbers are a widget in the view's left gutter.
"""

from __future__ import annotations

from collections.abc import Callable

import gi

gi.require_version("Gtk", "4.0")
gi.require_version("Adw", "1")
gi.require_version("Graphene", "1.0")
gi.require_version("Pango", "1.0")
from gi.repository import Adw, GLib, Graphene, Gtk, Pango  # noqa: E402

from ..client.config_tokenizer import Role, tokens  # noqa: E402
from ..client.secret_mask import placeholder_ranges  # noqa: E402
from .theme import Palette  # noqa: E402
from .widgets import name_widget  # noqa: E402

# colours: light, dark, light high contrast, dark high contrast
SECTION = Palette("#813d9c", "#dc8add", "#5a1d73", "#f5c2ff")
DIRECTIVE = Palette("#1c71d8", "#78aeed", "#0b4a9e", "#b9d6ff")
VALUE = Palette("#1a7f87", "#5fd4d4", "#0d5459", "#a6ffff")
MARKED = Palette("#c01c28", "#ff7b63", "#8a0f19", "#ffb3a3")


class ConfigEditor(Gtk.ScrolledWindow):
    def __init__(self, kind: int, on_edit: Callable[[str], None], accessible_name: str) -> None:
        super().__init__(vexpand=True, hexpand=True)
        self._kind = kind
        self._on_edit = on_edit
        self._marked_line: int | None = None
        self._loading = False
        self._restyle_scheduled = False

        self._view = Gtk.TextView(monospace=True, wrap_mode=Gtk.WrapMode.NONE, top_margin=10, bottom_margin=10, left_margin=6, right_margin=6)
        self._view.add_css_class("config-editor")
        name_widget(self._view, accessible_name)
        # Profile text holds keys while they are shown: no spelling or input method suggestions.
        self._view.set_input_hints(Gtk.InputHints.NO_SPELLCHECK | Gtk.InputHints.NO_EMOJI)
        self.set_child(self._view)
        self._buffer = self._view.get_buffer()
        self._tags = {
            "section": self._buffer.create_tag("section", weight=Pango.Weight.SEMIBOLD),
            "directive": self._buffer.create_tag("directive"),
            "value": self._buffer.create_tag("value"),
            "dim": self._buffer.create_tag("dim"),
            "placeholder": self._buffer.create_tag("placeholder"),
            "marked": self._buffer.create_tag("marked"),
        }
        self._by_role = {
            Role.SECTION: self._tags["section"],
            Role.BLOCK_TAG: self._tags["section"],
            Role.DIRECTIVE: self._tags["directive"],
            Role.NUMBER: self._tags["value"],
            Role.PORT: self._tags["value"],
            Role.IP_ADDRESS: self._tags["value"],
            Role.CIDR: self._tags["value"],
            Role.COMMENT: self._tags["dim"],
            Role.BASE64_KEY: self._tags["dim"],
            Role.BLOCK_BODY: self._tags["dim"],
        }

        self._numbers = _LineNumbers(self._view)
        self._view.set_gutter(Gtk.TextWindowType.LEFT, self._numbers)
        self.get_vadjustment().connect("value-changed", lambda *_: self._numbers.queue_draw())
        self._buffer.connect("changed", self._changed)
        self._buffer.connect("notify::cursor-position", lambda *_: self._numbers.queue_draw())

        manager = Adw.StyleManager.get_default()
        manager.connect("notify::dark", lambda *_: self._apply_palette())
        manager.connect("notify::high-contrast", lambda *_: self._apply_palette())
        self._view.connect("realize", lambda *_: self._apply_palette())

    # MARK: The model's side

    def text(self) -> str:
        return self._buffer.get_text(self._buffer.get_start_iter(), self._buffer.get_end_iter(), True)

    def set_text(self, text: str) -> None:
        """Shows what the model holds. The typing history is of another text."""
        if self.text() == text:
            return
        self._loading = True
        self._buffer.begin_irreversible_action()
        self._buffer.set_text(text)
        self._buffer.end_irreversible_action()
        self._loading = False
        self._restyle()

    def set_editable(self, editable: bool) -> None:
        self._view.set_editable(editable)

    def set_marked_line(self, line: int | None) -> None:
        """A line of the text (1-based) to mark as wrong."""
        if line != self._marked_line:
            self._marked_line = line
            self._numbers.marked_line = line
            self._restyle()

    def grab_editor_focus(self) -> None:
        self._view.grab_focus()

    # MARK: Editing

    def _changed(self, _buffer) -> None:
        self._numbers.update_width()
        if not self._loading:
            self._on_edit(self.text())
        self._schedule_restyle()

    def _schedule_restyle(self) -> None:
        if not self._restyle_scheduled:
            self._restyle_scheduled = True
            GLib.idle_add(self._restyle_now)

    def _restyle_now(self) -> bool:
        self._restyle_scheduled = False
        self._restyle()
        return GLib.SOURCE_REMOVE

    # MARK: Styling

    def _apply_palette(self) -> None:
        foreground = self._view.get_color()
        dim = foreground.copy()
        dim.alpha = 0.55
        self._tags["section"].set_property("foreground-rgba", SECTION.current())
        self._tags["directive"].set_property("foreground-rgba", DIRECTIVE.current())
        self._tags["value"].set_property("foreground-rgba", VALUE.current())
        self._tags["dim"].set_property("foreground-rgba", dim)
        placeholder = foreground.copy()
        placeholder.alpha = 0.2
        self._tags["placeholder"].set_property("background-rgba", placeholder)
        self._tags["placeholder"].set_property("foreground-rgba", dim)
        marked = MARKED.current().copy()
        marked.alpha = 0.22
        self._tags["marked"].set_property("background-rgba", marked)
        self._numbers.queue_draw()

    def _restyle(self) -> None:
        """Colours the text by what each part is: tags, which are drawn and are neither
        part of the text nor of its undo history."""
        buffer = self._buffer
        text = self.text()
        buffer.remove_all_tags(buffer.get_start_iter(), buffer.get_end_iter())

        def apply(tag, start: int, end: int) -> None:
            buffer.apply_tag(tag, buffer.get_iter_at_offset(start), buffer.get_iter_at_offset(end))

        for token in tokens(text, self._kind):
            tag = self._by_role.get(token.role)
            if tag is not None:
                apply(tag, token.start, token.end)
        for start, end in placeholder_ranges(text):
            apply(self._tags["placeholder"], start, end)
        if self._marked_line is not None:
            line = self._marked_line - 1
            if 0 <= line < buffer.get_line_count():
                start = buffer.get_iter_at_line(line)[1]
                end = start.copy()
                if not end.ends_line():
                    end.forward_to_line_end()
                buffer.apply_tag(self._tags["marked"], start, end)
        self._numbers.queue_draw()


class _LineNumbers(Gtk.Widget):
    """The numbers of the lines that are on screen, in the left gutter of the text view."""

    PADDING = 8

    def __init__(self, view: Gtk.TextView) -> None:
        super().__init__(accessible_role=Gtk.AccessibleRole.PRESENTATION)
        self._view = view
        self.marked_line: int | None = None
        self.add_css_class("line-numbers")
        self.update_width()

    def update_width(self) -> None:
        digits = max(3, len(str(self._view.get_buffer().get_line_count())))
        layout = self.create_pango_layout("9" * digits)
        width, _ = layout.get_pixel_size()
        self.set_size_request(width + 2 * self.PADDING + 4, -1)

    def do_snapshot(self, snapshot) -> None:
        view = self._view
        buffer = view.get_buffer()
        width = self.get_width()
        foreground = self.get_color()
        dim = foreground.copy()
        dim.alpha = 0.5
        current = buffer.get_iter_at_mark(buffer.get_insert()).get_line() if view.has_focus() else -1

        visible = view.get_visible_rect()
        iterator, _ = view.get_line_at_y(visible.y)
        bottom = visible.y + visible.height
        layout = self.create_pango_layout("")
        while True:
            top, height = view.get_line_yrange(iterator)
            if top > bottom:
                break
            number = iterator.get_line()
            layout.set_markup(self._markup(number, str(number + 1)), -1)
            text_width, text_height = layout.get_pixel_size()
            _, window_y = view.buffer_to_window_coords(Gtk.TextWindowType.LEFT, 0, top)
            snapshot.save()
            snapshot.translate(Graphene.Point().init(width - text_width - self.PADDING, window_y + (height - text_height) / 2))
            if number + 1 == self.marked_line:
                colour = _marked_colour()
            else:
                colour = foreground if number == current else dim
            snapshot.append_layout(layout, colour)
            snapshot.restore()
            if not iterator.forward_line():
                break

    def _markup(self, number: int, text: str) -> str:
        emphasised = number + 1 == self.marked_line
        return f"<b>{text}</b>" if emphasised else text


def _marked_colour():
    return MARKED.current()
