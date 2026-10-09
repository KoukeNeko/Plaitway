"""Small widgets and helpers that several pages use."""

from __future__ import annotations

from collections.abc import Callable, Iterable

import gi

gi.require_version("Gtk", "4.0")
gi.require_version("Adw", "1")
gi.require_version("Graphene", "1.0")
gi.require_version("Gsk", "4.0")
from gi.repository import Adw, Graphene, Gsk, Gtk  # noqa: E402

from ..core import presentation  # noqa: E402
from ..core.observable import Notifier  # noqa: E402
from ..core.presentation import Tone  # noqa: E402
from ..core.traffic_history import Sample  # noqa: E402
from . import style  # noqa: E402


ACCESSIBLE_NAME = "plaitway_accessible_name"


def name_widget(widget: Gtk.Widget, text: str) -> None:
    """What a screen reader says for a control that has no text of its own. It is kept on
    the widget as well, because GTK offers no way to read it back and the tests check it."""
    widget.update_property([Gtk.AccessibleProperty.LABEL], [text])
    setattr(widget, ACCESSIBLE_NAME, text)


def follow(widget: Gtk.Widget, notifiers: Iterable[Notifier], update: Callable[[], None]) -> None:
    """Calls `update` after each change of the models while `widget` is on screen, and
    once when it appears, so that a page that was away catches up."""
    notifiers = list(notifiers)
    disconnects: list[Callable[[], None]] = []

    def connect(*_) -> None:
        if not disconnects:
            disconnects.extend(notifier.connect(update) for notifier in notifiers)
        update()

    def disconnect(*_) -> None:
        for cancel in disconnects:
            cancel()
        disconnects.clear()

    widget.connect("map", connect)
    widget.connect("unmap", disconnect)
    if widget.get_mapped():
        connect()


def label(
    text: str = "",
    *,
    css: Iterable[str] = (),
    wrap: bool = False,
    selectable: bool = False,
    xalign: float = 0.0,
    ellipsize=None,
    mono: bool = False,
) -> Gtk.Label:
    widget = Gtk.Label(label=text, xalign=xalign, wrap=wrap, selectable=selectable)
    if ellipsize is not None:
        widget.set_ellipsize(ellipsize)
    for name in css:
        widget.add_css_class(name)
    if mono:
        widget.add_css_class("mono")
    return widget


def icon_button(icon: str, name: str, on_click: Callable[[], None], *, css: Iterable[str] = ("flat",)) -> Gtk.Button:
    """A button that is only an icon: its name is its tooltip and what a screen reader says."""
    button = Gtk.Button(icon_name=icon, tooltip_text=name, valign=Gtk.Align.CENTER)
    name_widget(button, name)
    for css_class in css:
        button.add_css_class(css_class)
    button.connect("clicked", lambda _: on_click())
    return button


def text_button(text: str, on_click: Callable[[], None], *, css: Iterable[str] = ()) -> Gtk.Button:
    button = Gtk.Button(label=text, valign=Gtk.Align.CENTER)
    for css_class in css:
        button.add_css_class(css_class)
    button.connect("clicked", lambda _: on_click())
    return button


class StateGlyph(Gtk.Image):
    """A state's shape in the colour of its tone. It moves while the state is
    transitional (the CSS animation stops when animations are off)."""

    def __init__(self, pixel_size: int = 16) -> None:
        # The word next to it says the state; assistive technology does not read the shape.
        super().__init__(pixel_size=pixel_size, accessible_role=Gtk.AccessibleRole.PRESENTATION)

    def show_profile_state(self, state: int) -> None:
        self.set_from_icon_name(presentation.profile_glyph(state))
        style.set_tone(self, presentation.profile_tone(state))
        if presentation.is_transitional(state):
            self.add_css_class("glyph-pulse")
        else:
            self.remove_css_class("glyph-pulse")

    def show_route_state(self, state: int) -> None:
        self.set_from_icon_name(presentation.route_glyph(state))
        style.set_tone(self, presentation.route_tone(state))


class StatusLabel(Gtk.Box):
    """A glyph and the word for the state, which is what assistive technology reads."""

    def __init__(self, route_state: int, text: str) -> None:
        super().__init__(spacing=6, valign=Gtk.Align.CENTER)
        glyph = StateGlyph()
        glyph.show_route_state(route_state)
        self.append(glyph)
        self.append(label(text))


class NoticeBar(Gtk.Box):
    """A note above a page: an icon, and what there is to say."""

    def __init__(self, tone: Tone, icon: str) -> None:
        super().__init__(spacing=10)
        self.add_css_class("notice")
        if tone is not Tone.NORMAL:
            self.add_css_class(style.TONE_CLASSES[tone] or "")
        self.append(Gtk.Image(icon_name=icon, valign=Gtk.Align.START, accessible_role=Gtk.AccessibleRole.PRESENTATION))
        self.body = Gtk.Box(spacing=8, hexpand=True)
        self.append(self.body)

    def set_messages(self, parts: list[Gtk.Widget]) -> None:
        child = self.body.get_first_child()
        while child is not None:
            following = child.get_next_sibling()
            self.body.remove(child)
            child = following
        for part in parts:
            self.body.append(part)


def page_bar() -> Gtk.Box:
    """What a page can do, in a strip at its top: not the header bar, which belongs to the window."""
    bar = Gtk.Box(spacing=8)
    bar.add_css_class("page-bar")
    return bar


def status_page(icon: str, title: str, description: str | None = None) -> Adw.StatusPage:
    page = Adw.StatusPage(icon_name=icon, title=title)
    if description:
        page.set_description(description)
    return page


def clear(box: Gtk.Box) -> None:
    child = box.get_first_child()
    while child is not None:
        following = child.get_next_sibling()
        box.remove(child)
        child = following


class Sparkline(Gtk.Widget):
    """A sparkline of both directions on one scale: received as a filled area, sent
    as a dashed line, so that the two differ by shape and not by colour. It has no
    axes; the numbers are above it."""

    FLOOR = 1024.0  # bytes per second: a quiet line does not fill the graph

    def __init__(self, capacity: int) -> None:
        super().__init__(hexpand=True, accessible_role=Gtk.AccessibleRole.PRESENTATION)
        self._capacity = capacity
        self._samples: list[Sample] = []
        self.set_size_request(-1, 56)

    def set_samples(self, samples: list[Sample]) -> None:
        if samples != self._samples:
            self._samples = samples
            self.queue_draw()

    def do_snapshot(self, snapshot) -> None:
        width, height = self.get_width(), self.get_height()
        foreground = self.get_color()
        accent = Adw.StyleManager.get_default().get_accent_color_rgba()

        background = foreground.copy()
        background.alpha = 0.07
        outline = Gsk.RoundedRect()
        outline.init_from_rect(Graphene.Rect().init(0, 0, width, height), 6)
        snapshot.push_rounded_clip(outline)
        snapshot.append_color(background, Graphene.Rect().init(0, 0, width, height))
        if len(self._samples) >= 2:
            self._draw(snapshot, width, height, foreground, accent)
        snapshot.pop()

    def _draw(self, snapshot, width: int, height: int, foreground, accent) -> None:
        peak = max(max(max(s.received, s.sent) for s in self._samples), self.FLOOR)
        count = len(self._samples)
        step = width / max(self._capacity - 1, 1)
        left = width - (count - 1) * step  # the newest sample is at the right edge

        def point(index: int, value: float) -> tuple[float, float]:
            return left + index * step, height - 3 - (height - 6) * value / peak

        area = Gsk.PathBuilder.new()
        area.move_to(left, height)
        for index, sample in enumerate(self._samples):
            area.line_to(*point(index, sample.received))
        area.line_to(width, height)
        area.close()
        fill = accent.copy()
        fill.alpha = 0.25
        snapshot.append_fill(area.to_path(), Gsk.FillRule.WINDING, fill)

        received = Gsk.PathBuilder.new()
        sent = Gsk.PathBuilder.new()
        for index, sample in enumerate(self._samples):
            (received.move_to if index == 0 else received.line_to)(*point(index, sample.received))
            (sent.move_to if index == 0 else sent.line_to)(*point(index, sample.sent))
        snapshot.append_stroke(received.to_path(), Gsk.Stroke.new(1.5), accent)
        dashed = Gsk.Stroke.new(1.5)
        dashed.set_dash([4.0, 3.0])
        dim = foreground.copy()
        dim.alpha = 0.7
        snapshot.append_stroke(sent.to_path(), dashed, dim)


class PageSwitcher(Gtk.Box):
    """The pages of what is selected, as a row of toggles in the header."""

    def __init__(self, on_chosen: Callable[[str], None]) -> None:
        super().__init__()
        self._on_chosen = on_chosen
        self._names: list[str] = []
        self._syncing = False
        self._group = Adw.ToggleGroup()
        self._group.connect("notify::active-name", self._changed)
        self.append(self._group)

    def show_pages(self, pages: list[tuple[str, str]], active: str | None) -> None:
        """`pages` is (name, label) in order; `active` the name that is open."""
        self._syncing = True
        try:
            names = [name for name, _ in pages]
            if names != self._names:
                while self._group.get_n_toggles():
                    self._group.remove(self._group.get_toggle(0))
                for name, text in pages:
                    self._group.add(Adw.Toggle(name=name, label=text))
                self._names = names
            if active != self._group.get_active_name():
                self._group.set_active_name(active)
        finally:
            self._syncing = False

    def _changed(self, *_) -> None:
        name = self._group.get_active_name()
        if not self._syncing and name:
            self._on_chosen(name)
