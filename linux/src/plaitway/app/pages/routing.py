"""The Routing page: routes on the left, tunnels on the right, one line per request. A line is solid
where the tunnel holds the route and dotted with a cross where it waits behind a higher priority.
In a narrow window the page is a list of routes, each with a chip per tunnel that asks for it.

It draws what the daemon says (`core.routing`) and hands the commands to the app model; it decides
nothing about who holds what. The Windows app has the same page (`windows/rust/plaitway-ui/src/bay.rs`).
"""

from __future__ import annotations

import math
import time

import gi

gi.require_version("Gtk", "4.0")
gi.require_version("Adw", "1")
gi.require_version("Graphene", "1.0")
gi.require_version("Gsk", "4.0")
from gi.repository import Gdk, GLib, Graphene, Gsk, Gtk, Pango  # noqa: E402

from ...client.profile_set import ProfileSet  # noqa: E402
from ...client.types import Profile, ProfileState  # noqa: E402
from ...core import routing  # noqa: E402
from ...core.app_model import AppModel  # noqa: E402
from ...core.routing import BayRoute, Standing  # noqa: E402
from ..theme import HUES  # noqa: E402
from ..widgets import HueDot, follow, label, name_widget  # noqa: E402

WIDE_FROM = 640
ROUTE_W = 260
ROUTE_H = 56
ROUTE_GAP = 16
TUNNEL_H = 104
TUNNEL_GAP = 14
CHIP_ROUTE_H = 78
CHIP_TUNNEL_H = 44
CHIP_GAP = 8
SECTION_GAP = 20
# Where the lines of a tunnel's requests meet its card, from its top and apart from one another.
LINE_TOP = 26
LINE_PITCH = 14
DRAW_IN = 0.6

Rect = tuple[float, float, float, float]


class RouteCard(Gtk.Button):
    """A route: its name and who asks for it. Pressing it selects it."""

    def __init__(self, on_press) -> None:
        super().__init__()
        self.add_css_class("bay-card")
        self.name = label(css=["heading"], ellipsize=Pango.EllipsizeMode.END)
        self.description = label(css=["caption", "dimmed"], ellipsize=Pango.EllipsizeMode.END)
        self.chips = Gtk.Box(spacing=6, halign=Gtk.Align.START)
        self.holder = HueDot(10)
        words = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=2, valign=Gtk.Align.CENTER, hexpand=True)
        words.append(self.name)
        words.append(self.description)
        words.append(self.chips)
        row = Gtk.Box(spacing=8)
        row.append(words)
        row.append(self.holder)
        self.set_child(row)
        self.connect("clicked", lambda _: on_press())


class TunnelCard(Gtk.Box):
    """A tunnel: its colour, its name, how it stands and the button that connects or disconnects it.
    The Bay draws the tint behind it."""

    def __init__(self, model: AppModel, profile_id: str) -> None:
        super().__init__(orientation=Gtk.Orientation.VERTICAL, spacing=4)
        self.profile_id = profile_id
        self.add_css_class("bay-tunnel")
        self.dot = HueDot(10)
        self.name = label(css=["heading"], ellipsize=Pango.EllipsizeMode.END)
        self.kind = label(css=["caption", "bay-pill"])
        self.state = label(css=["caption", "dimmed"], ellipsize=Pango.EllipsizeMode.END)
        self.button = Gtk.Button(halign=Gtk.Align.END, valign=Gtk.Align.END, vexpand=True)
        self.button.connect("clicked", lambda _: self._toggle(model))
        top = Gtk.Box(spacing=8)
        top.append(self.dot)
        self.name.set_hexpand(True)
        top.append(self.name)
        top.append(self.kind)
        self._top = top
        self.append(top)
        self.append(self.state)
        self.append(self.button)

    def _toggle(self, model: AppModel) -> None:
        profile = model.store.profile(self.profile_id)
        if profile is not None:
            model.toggle(profile)


class Bay(Gtk.Widget):
    """The routes, the tunnels and the lines between them. Geometry is in logical pixels."""

    __gtype_name__ = "PlaitwayBay"

    def __init__(self, model: AppModel) -> None:
        super().__init__(hexpand=True)
        self._model = model
        self._strings = model.strings
        self._profiles = ProfileSet()
        self._routes: list[BayRoute] = []
        self._route_cards: dict[str, RouteCard] = {}
        self._tunnel_cards: dict[str, TunnelCard] = {}
        self.selected = 0
        self._hover: tuple[str, int] | None = None
        self._signature: tuple | None = None
        self._drawn_at: float | None = None
        self._tick = 0
        self._compact = False
        self._on_selected = lambda: None

    # MARK: Model to view

    def connect_selected(self, callback) -> None:
        self._on_selected = callback

    def update(self, profiles: ProfileSet) -> None:
        self._profiles = profiles
        self._routes = routing.routes_of(profiles)
        self.selected = max(0, min(self.selected, len(self._routes) - 1))
        signature = tuple(
            (p.id, p.desired_enabled, p.state, tuple((r.prefix, r.state) for r in p.status.routes)) for p in profiles
        )
        if signature != self._signature:
            self._signature = signature
            self._restart_animation()
        self._sync_cards(self._route_cards, [r.prefix for r in self._routes], self._new_route_card)
        self._sync_cards(self._tunnel_cards, profiles.ids, self._new_tunnel_card)
        for index, route in enumerate(self._routes):
            self._show_route(self._route_cards[route.prefix], index, route)
        for rank, profile in enumerate(profiles, start=1):
            self._show_tunnel(self._tunnel_cards[profile.id], profile, rank)
        self.queue_resize()
        self.queue_draw()

    def _sync_cards(self, cards: dict, keys: list[str], make) -> None:
        for key in [key for key in cards if key not in keys]:
            cards.pop(key).unparent()
        for key in keys:
            if key not in cards:
                cards[key] = make(key)
                cards[key].set_parent(self)

    def _new_route_card(self, prefix: str) -> RouteCard:
        card = RouteCard(lambda: self._select_prefix(prefix))
        motion = Gtk.EventControllerMotion()
        motion.connect("enter", lambda *_: self._set_hover("route", prefix))
        motion.connect("leave", lambda *_: self._set_hover(None, ""))
        card.add_controller(motion)
        return card

    def _new_tunnel_card(self, profile_id: str) -> TunnelCard:
        card = TunnelCard(self._model, profile_id)
        motion = Gtk.EventControllerMotion()
        motion.connect("enter", lambda *_: self._set_hover("tunnel", profile_id))
        motion.connect("leave", lambda *_: self._set_hover(None, ""))
        card.add_controller(motion)
        return card

    def _select_prefix(self, prefix: str) -> None:
        for index, route in enumerate(self._routes):
            if route.prefix == prefix:
                self.selected = index
        self.update(self._profiles)
        self._on_selected()

    def _show_route(self, card: RouteCard, index: int, route: BayRoute) -> None:
        s = self._strings
        name, _ = routing.route_title(route.prefix, s)
        card.name.set_text(name)
        card.description.set_text(routing.route_description(route, self._profiles, s))
        card.holder.set_visible(not self._compact and route.holder is not None)
        if route.holder is not None:
            card.holder.show_hue(routing.hue_index(self._profiles, self._profiles[route.holder].id), True)
        child = card.chips.get_first_child()
        while child is not None:
            following = child.get_next_sibling()
            card.chips.remove(child)
            child = following
        card.chips.set_visible(self._compact)
        if self._compact:
            for tunnel, standing in route.requests:
                profile = self._profiles[tunnel]
                chip = Gtk.Box(spacing=6)
                chip.add_css_class("bay-chip")
                words = label(routing.chip_text(standing, profile.name, s), css=["caption"])
                if standing is Standing.HOLDS:
                    dot = HueDot(8)
                    dot.show_hue(routing.hue_index(self._profiles, profile.id), True)
                    chip.append(dot)
                else:
                    words.add_css_class("dimmed")
                chip.append(words)
                card.chips.append(chip)
        name_widget(card, routing.why(route, self._profiles, s))
        if index == self.selected:
            card.add_css_class("selected")
        else:
            card.remove_css_class("selected")

    def _show_tunnel(self, card: TunnelCard, profile: Profile, rank: int) -> None:
        s = self._strings
        on = profile.desired_enabled
        if self._compact:
            card.add_css_class("compact")
        else:
            card.remove_css_class("compact")
        card.dot.show_hue(routing.hue_index(self._profiles, profile.id), on)
        card.name.set_text(f"{profile.name} · {s.on if on else s.off}" if self._compact else profile.name)
        card.kind.set_text(self._model.presenter.kind_text(profile.kind))
        card.kind.set_visible(not self._compact)
        card.state.set_text(routing.state_line(profile, rank, s))
        card.state.set_visible(not self._compact)
        card.button.set_visible(not self._compact)
        card.button.set_label(s.disconnect if on else s.connect)
        card.button.set_sensitive(profile.state != ProfileState.DISCONNECTING)

    # MARK: Hover

    def _set_hover(self, kind: str | None, key: str) -> None:
        hover = (kind, key) if kind else None
        if hover != self._hover:
            self._hover = hover
            self.queue_draw()

    # MARK: Layout

    def _layout(self, width: float) -> tuple[list[Rect], list[Rect], float]:
        """The rectangles of the routes and of the tunnels (in the order of the profiles), and the height."""
        n_routes, n_tunnels = len(self._routes), len(self._profiles)
        if width < WIDE_FROM:
            each = max(40.0, (width - CHIP_GAP * (max(n_tunnels, 1) - 1)) / max(n_tunnels, 1))
            tunnels = [(k * (each + CHIP_GAP), 0.0, each, CHIP_TUNNEL_H) for k in range(n_tunnels)]
            top = CHIP_TUNNEL_H + SECTION_GAP if n_tunnels else 0.0
            routes = [(0.0, top + i * (CHIP_ROUTE_H + 10), width, CHIP_ROUTE_H) for i in range(n_routes)]
            bottom = routes[-1][1] + CHIP_ROUTE_H if routes else top
            return routes, tunnels, bottom
        tunnel_w = min(max(width * 0.28, 250.0), 320.0)
        routes = [(0.0, i * (ROUTE_H + ROUTE_GAP), ROUTE_W, ROUTE_H) for i in range(n_routes)]
        tunnels = [(width - tunnel_w, k * (TUNNEL_H + TUNNEL_GAP), tunnel_w, TUNNEL_H) for k in range(n_tunnels)]
        bottom = max(routes[-1][1] + ROUTE_H if routes else 0.0, tunnels[-1][1] + TUNNEL_H if tunnels else 0.0)
        return routes, tunnels, bottom

    def do_measure(self, orientation, for_size):
        child = self.get_first_child()
        while child is not None:
            child.measure(orientation, -1)
            child = child.get_next_sibling()
        if orientation == Gtk.Orientation.HORIZONTAL:
            return 280, 700, -1, -1
        height = int(math.ceil(self._layout(for_size if for_size > 0 else 700)[2]))
        return height, height, -1, -1

    def do_size_allocate(self, width: int, height: int, baseline: int) -> None:
        if (width < WIDE_FROM) != self._compact:
            # The cards say less in the compact layout; they are told before they get their size.
            self._compact = width < WIDE_FROM
            self.update(self._profiles)
        routes, tunnels, _ = self._layout(width)
        for route, rect in zip(self._routes, routes):
            self._place(self._route_cards[route.prefix], rect)
        for profile, rect in zip(self._profiles, tunnels):
            self._place(self._tunnel_cards[profile.id], rect)

    @staticmethod
    def _place(card: Gtk.Widget, rect: Rect) -> None:
        area = Gdk.Rectangle()
        area.x, area.y, area.width, area.height = (int(round(value)) for value in rect)
        card.size_allocate(area, -1)

    def do_dispose(self) -> None:
        for cards in (self._route_cards, self._tunnel_cards):
            for card in cards.values():
                card.unparent()
            cards.clear()
        Gtk.Widget.do_dispose(self)

    # MARK: Drawing

    def _restart_animation(self) -> None:
        if not Gtk.Settings.get_default().props.gtk_enable_animations:
            self._drawn_at = None
            return
        self._drawn_at = time.monotonic()
        if not self._tick:
            self._tick = self.add_tick_callback(self._animate)

    def _animate(self, *_) -> bool:
        self.queue_draw()
        if self._progress() >= 1.0:
            self._tick = 0
            return GLib.SOURCE_REMOVE
        return GLib.SOURCE_CONTINUE

    def _progress(self) -> float:
        if self._drawn_at is None:
            return 1.0
        p = min((time.monotonic() - self._drawn_at) / DRAW_IN, 1.0)
        return 1.0 - (1.0 - p) * (1.0 - p)

    def do_snapshot(self, snapshot) -> None:
        width = self.get_width()
        routes, tunnels, _ = self._layout(width)
        ink = self.get_color()
        for profile, rect in zip(self._profiles, tunnels):
            self._tint_tunnel(snapshot, profile, rect, ink, 10 if width >= WIDE_FROM else 8)
        if width >= WIDE_FROM:
            self._draw_lines(snapshot, routes, tunnels, ink)
        child = self.get_first_child()
        while child is not None:
            self.snapshot_child(child, snapshot)
            child = child.get_next_sibling()

    def _hue(self, profile_id: str) -> Gdk.RGBA:
        return HUES[routing.hue_index(self._profiles, profile_id)].current()

    def _tint_tunnel(self, snapshot, profile: Profile, rect: Rect, ink: Gdk.RGBA, radius: float) -> None:
        x, y, w, h = rect
        shape = Gsk.RoundedRect()
        shape.init_from_rect(Graphene.Rect().init(x, y, w, h), radius)
        if profile.desired_enabled:
            hue = self._hue(profile.id)
            snapshot.push_rounded_clip(shape)
            snapshot.append_color(with_alpha(hue, 0.13), shape.bounds)
            snapshot.pop()
            edge = with_alpha(hue, 0.28)
        else:
            edge = with_alpha(ink, 0.15)
        snapshot.append_border(shape, [1, 1, 1, 1], [edge] * 4)

    def _draw_lines(self, snapshot, routes: list[Rect], tunnels: list[Rect], ink: Gdk.RGBA) -> None:
        progress = self._progress()
        for slot, profile in enumerate(self._profiles):
            hue = self._hue(profile.id)
            n = 0
            for index, route in enumerate(self._routes):
                standing = next((st for t, st in route.requests if t == slot), None)
                if standing is None:
                    continue
                lit = self._lit(route, slot)
                alpha = (1.0 if standing is Standing.HOLDS else 0.7) if lit else 0.12
                x0, y0 = routes[index][0] + ROUTE_W, routes[index][1] + ROUTE_H / 2
                x1, y1 = tunnels[slot][0], tunnels[slot][1] + LINE_TOP + n * LINE_PITCH
                n += 1
                points = _bezier(x0, y0, x1, y1)
                shown = points[: math.ceil((len(points) - 1) * progress) + 1]
                stroke = Gsk.Stroke.new(3 if standing is Standing.HOLDS else 2.5)
                stroke.set_line_cap(Gsk.LineCap.ROUND)
                if standing is not Standing.HOLDS:
                    stroke.set_dash([0.2 * 2.5, 2.8 * 2.5])
                snapshot.append_stroke(_path(shown), stroke, with_alpha(hue, alpha))
                if standing is not Standing.HOLDS and progress >= 0.85:
                    px, py = points[len(points) * 4 // 5]
                    cross = Gsk.PathBuilder.new()
                    cross.move_to(px - 4.5, py - 4.5)
                    cross.line_to(px + 4.5, py + 4.5)
                    cross.move_to(px - 4.5, py + 4.5)
                    cross.line_to(px + 4.5, py - 4.5)
                    stroke = Gsk.Stroke.new(2)
                    stroke.set_line_cap(Gsk.LineCap.ROUND)
                    snapshot.append_stroke(cross.to_path(), stroke, with_alpha(ink, alpha))

    def _lit(self, route: BayRoute, slot: int) -> bool:
        """Hovering a route or a tunnel dims every other line."""
        if self._hover is None:
            return True
        kind, key = self._hover
        if kind == "route":
            return route.prefix == key
        return self._profiles[slot].id == key


def with_alpha(colour: Gdk.RGBA, alpha: float) -> Gdk.RGBA:
    result = colour.copy()
    result.alpha = alpha
    return result


def _path(points: list[tuple[float, float]]) -> Gsk.Path:
    builder = Gsk.PathBuilder.new()
    builder.move_to(*points[0])
    for point in points[1:]:
        builder.line_to(*point)
    return builder.to_path()


def _bezier(x0: float, y0: float, x1: float, y1: float, steps: int = 28) -> list[tuple[float, float]]:
    """A cubic with horizontal tangents at both ends, flattened."""
    dx = (x1 - x0) / 2
    c0, c1 = (x0 + dx, y0), (x1 - dx, y1)
    points = []
    for i in range(steps + 1):
        t = i / steps
        u = 1 - t
        a, b, c, d = u * u * u, 3 * u * u * t, 3 * u * t * t, t * t * t
        points.append((a * x0 + b * c0[0] + c * c1[0] + d * x1, a * y0 + b * c0[1] + c * c1[1] + d * y1))
    return points


class RoutingPage(Gtk.ScrolledWindow):
    def __init__(self, model: AppModel) -> None:
        super().__init__(hscrollbar_policy=Gtk.PolicyType.NEVER)
        self._model = model
        s = model.strings
        box = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=4, margin_top=24, margin_bottom=24, margin_start=24, margin_end=24)
        self.set_child(box)
        box.append(label(s.routing, css=["title-1"]))
        self._summary = label(css=["dimmed"])
        box.append(self._summary)
        self._bay = Bay(model)
        self._bay.set_margin_top(20)
        self._bay.connect_selected(self._show_why)
        box.append(self._bay)
        self._why = label(wrap=True, css=["bay-why"])
        why_strip = Gtk.Box(margin_top=24)
        why_strip.add_css_class("bay-card")
        why_strip.append(self._why)
        box.append(why_strip)
        follow(self, [model.changed], self.update)

    def update(self) -> None:
        profiles = self._model.store.profiles
        self._bay.update(profiles)
        routes = routing.routes_of(profiles)
        held = sum(1 for route in routes if route.holder is not None)
        self._summary.set_text(self._model.strings.routes_held(held, len(routes)))
        self._show_why()

    def _show_why(self) -> None:
        profiles = self._model.store.profiles
        routes = routing.routes_of(profiles)
        selected = self._bay.selected
        self._why.set_text(
            routing.why(routes[selected], profiles, self._model.strings) if routes else self._model.strings.no_route_is_asked_for_yet
        )
