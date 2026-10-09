"""The Overview page of a profile: the state and why it is so, the uptime, the rate
in and out with a two minute graph, what is not in effect, and the addresses."""

from __future__ import annotations

import time
from collections.abc import Callable

import gi

gi.require_version("Gtk", "4.0")
gi.require_version("Adw", "1")
from gi.repository import Adw, GLib, Gtk  # noqa: E402

from ...client.types import Profile, ProfileState  # noqa: E402
from ...core import formatting, presentation  # noqa: E402
from ...core.app_model import AppModel  # noqa: E402
from ...core.presentation import Tone  # noqa: E402
from ...core.traffic_history import CAPACITY  # noqa: E402
from .. import style  # noqa: E402
from ..widgets import Sparkline, StateGlyph, follow, icon_button, label, text_button  # noqa: E402


class OverviewPage(Adw.PreferencesPage):
    def __init__(self, model: AppModel, profile_id: str, show_routes: Callable[[], None]) -> None:
        super().__init__()
        self._model = model
        self._profile_id = profile_id
        self._show_routes = show_routes
        self._strings = model.strings
        self._attention_rows: list[Gtk.Widget] = []
        self._uptime_source = 0
        self._connected_since: float | None = None

        self._build_hero()
        self._build_attention()
        self._build_traffic()
        self._build_details()

        follow(self, [model.changed], self.update)
        follow(self, [model.traffic_changed], self.update_traffic)
        self.connect("map", self._start_uptime)
        self.connect("unmap", self._stop_uptime)

    # MARK: Building

    def _build_hero(self) -> None:
        """The glyph sits on a tile of its colour, centred against the words, and the
        uptime is laid out as the rates below it are, the name above its value."""
        self._hero = Adw.PreferencesGroup()
        box = Gtk.Box(spacing=16, margin_top=6, margin_bottom=6, hexpand=True)
        self._glyph = StateGlyph(28)
        self._tile = Gtk.CenterBox(halign=Gtk.Align.START, valign=Gtk.Align.CENTER)
        self._tile.add_css_class("state-tile")
        self._tile.set_center_widget(self._glyph)
        box.append(self._tile)

        words = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=4, valign=Gtk.Align.CENTER, hexpand=True)
        self._state = label(css=["title-2"], wrap=True)
        self._cause = label(wrap=True, selectable=True)
        words.append(self._state)
        words.append(self._cause)
        box.append(words)

        self._uptime_box = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=2, valign=Gtk.Align.CENTER, halign=Gtk.Align.END)
        self._uptime_box.append(label(self._strings.uptime, css=["caption", "dimmed"], xalign=1))
        self._uptime = label(css=["title-3", "numeric"], xalign=1)
        self._uptime_box.append(self._uptime)
        box.append(self._uptime_box)
        self._hero.add(box)
        self.add(self._hero)

    def _build_attention(self) -> None:
        self._attention = Adw.PreferencesGroup(title=self._strings.attention)
        self.add(self._attention)

    def _build_traffic(self) -> None:
        self._traffic = Adw.PreferencesGroup()
        box = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=10)
        columns = Gtk.Box(spacing=24)
        self._received = _Direction("go-down-symbolic", self._strings.received)
        self._sent = _Direction("go-up-symbolic", self._strings.sent)
        columns.append(self._received)
        columns.append(self._sent)
        box.append(columns)
        self._graph = Sparkline(CAPACITY)
        box.append(self._graph)
        self._traffic.add(box)
        self.add(self._traffic)

    def _build_details(self) -> None:
        s = self._strings
        self._details = Adw.PreferencesGroup()
        self._remote = self._property_row(s.remote)
        self._interface = self._property_row(s.interface)
        self._addresses = self._property_row(s.addresses)
        self._dns = self._property_row(s.dns)
        self._public_key = self._property_row(s.public_key)
        self._public_key.add_suffix(icon_button("edit-copy-symbolic", s.copy, self._copy_public_key))
        self._public_key.set_subtitle_lines(1)
        for row in (self._remote, self._interface, self._addresses, self._dns, self._public_key):
            self._details.add(row)
        self.add(self._details)

    @staticmethod
    def _property_row(title: str) -> Adw.ActionRow:
        row = Adw.ActionRow(title=title, subtitle_selectable=True, subtitle_lines=0)
        row.add_css_class("property")
        return row

    # MARK: Updating

    @property
    def _profile(self) -> Profile | None:
        return self._model.store.profile(self._profile_id)

    def update(self) -> None:
        profile = self._profile
        if profile is None:
            return
        presenter = self._model.presenter
        self._glyph.show_profile_state(profile.state)
        tone = presentation.profile_tone(profile.state)
        for css in ("success", "warning", "error"):
            self._tile.remove_css_class(css)
        if style.TONE_CLASSES[tone] in ("success", "warning", "error"):
            self._tile.add_css_class(style.TONE_CLASSES[tone])
        self._state.set_text(presenter.state_label(profile.state))
        self._cause.set_text(profile.last_error)
        self._cause.set_visible(bool(profile.last_error))
        style.set_tone(self._cause, Tone.ERROR if profile.state == ProfileState.FAILED else Tone.MUTED)

        connected = profile.state == ProfileState.CONNECTED
        since = profile.status.connected_since
        self._connected_since = since.seconds + since.nanos / 1e9 if connected and profile.status.HasField("connected_since") else None
        self._uptime_box.set_visible(self._connected_since is not None)
        self._tick_uptime()

        self._update_attention(profile)
        self._traffic.set_visible(connected)
        self.update_traffic()
        self._update_details(profile)

    def _update_attention(self, profile: Profile) -> None:
        for row in self._attention_rows:
            self._attention.remove(row)
        self._attention_rows.clear()
        s = self._strings
        lost = [route for route in profile.status.routes if presentation.is_lost(route.state)]
        if lost:
            row = Adw.ActionRow(title=s.routes_not_in_effect)
            row.add_prefix(_warning_icon())
            row.add_suffix(label(str(len(lost)), css=["numeric"]))
            row.add_suffix(text_button(s.show, self._show_routes, css=["flat"]))
            self._attention.add(row)
            self._attention_rows.append(row)
        for warning in profile.status.warnings:
            row = Adw.ActionRow(title=GLib.markup_escape_text(warning), title_lines=0, title_selectable=True)
            row.add_prefix(_warning_icon())
            self._attention.add(row)
            self._attention_rows.append(row)
        self._attention.set_visible(bool(self._attention_rows))

    def update_traffic(self) -> None:
        profile = self._profile
        if profile is None or profile.state != ProfileState.CONNECTED:
            return
        rate = self._model.traffic.rate(self._profile_id)
        strings = self._strings
        self._received.show(
            formatting.format_rate(rate.received, strings) if rate else "–", formatting.format_bytes(profile.status.rx_bytes)
        )
        self._sent.show(
            formatting.format_rate(rate.sent, strings) if rate else "–", formatting.format_bytes(profile.status.tx_bytes)
        )
        self._graph.set_samples(self._model.traffic.samples(self._profile_id))

    def _update_details(self, profile: Profile) -> None:
        # The endpoint in use, or the ones the profile names while it is not connected.
        remote = profile.status.remote or "\n".join(
            formatting.format_endpoint(e.host, e.port, e.protocol) for e in profile.summary.endpoints
        )
        addresses = ", ".join(profile.status.addresses or profile.summary.addresses)
        for row, value in [
            (self._remote, remote),
            (self._interface, profile.status.interface_name),
            (self._addresses, addresses),
            (self._dns, ", ".join(profile.summary.dns_servers)),
            (self._public_key, profile.summary.public_key),
        ]:
            row.set_visible(bool(value))
            row.set_subtitle(GLib.markup_escape_text(value))
        self._details.set_visible(any(row.get_visible() for row in (self._remote, self._interface, self._addresses, self._dns, self._public_key)))

    def _copy_public_key(self) -> None:
        if self._profile is not None:
            self._model.copy(self._profile.summary.public_key)

    # MARK: Uptime

    def _start_uptime(self, *_) -> None:
        if not self._uptime_source:
            self._uptime_source = GLib.timeout_add_seconds(1, self._tick_uptime)

    def _stop_uptime(self, *_) -> None:
        if self._uptime_source:
            GLib.source_remove(self._uptime_source)
            self._uptime_source = 0

    def _tick_uptime(self) -> bool:
        if self._connected_since is not None:
            self._uptime.set_text(formatting.format_uptime(time.time() - self._connected_since))
            self._uptime.set_tooltip_text(formatting.format_date_time(self._connected_since))
        return GLib.SOURCE_CONTINUE


class _Direction(Gtk.Box):
    """The rate in one direction, with the total under it."""

    def __init__(self, icon: str, name: str) -> None:
        super().__init__(orientation=Gtk.Orientation.VERTICAL, spacing=2)
        heading = Gtk.Box(spacing=4)
        heading.add_css_class("dimmed")
        heading.add_css_class("caption")
        heading.append(Gtk.Image(icon_name=icon, pixel_size=12, accessible_role=Gtk.AccessibleRole.PRESENTATION))
        heading.append(label(name))
        self._rate = label(css=["rate", "numeric"])
        self._total = label(css=["caption", "dimmed", "numeric"])
        self.append(heading)
        self.append(self._rate)
        self.append(self._total)

    def show(self, rate: str, total: str) -> None:
        self._rate.set_text(rate)
        self._total.set_text(total)


def _warning_icon() -> Gtk.Image:
    icon = Gtk.Image(icon_name="dialog-warning-symbolic", accessible_role=Gtk.AccessibleRole.PRESENTATION)
    icon.add_css_class("warning")
    return icon
