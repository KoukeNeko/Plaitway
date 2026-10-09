"""What the profile has put in the routing table and in DNS, and for each entry
whether it is in effect and, when it is not, why."""

from __future__ import annotations

import gi

gi.require_version("Gtk", "4.0")
gi.require_version("Adw", "1")
from gi.repository import Adw, GLib, Gtk  # noqa: E402

from ...core.app_model import AppModel  # noqa: E402
from ...core.presentation import DnsRow, RouteRow  # noqa: E402
from ..widgets import StatusLabel, follow, status_page  # noqa: E402


def _mono(text: str) -> str:
    return f"<tt>{GLib.markup_escape_text(text)}</tt>"


class RoutesPage(Gtk.Stack):
    def __init__(self, model: AppModel, profile_id: str) -> None:
        super().__init__()
        self._model = model
        self._profile_id = profile_id
        self._strings = model.strings
        self._shown: tuple | None = None
        self._rows: list[tuple[Adw.PreferencesGroup, Adw.ActionRow]] = []

        self._empty = status_page("network-workgroup-symbolic", self._strings.no_routes)
        self.add_named(self._empty, "empty")
        self._page = Adw.PreferencesPage()
        self._routes = Adw.PreferencesGroup(title=self._strings.routes)
        self._dns = Adw.PreferencesGroup(title=self._strings.dns)
        self._page.add(self._routes)
        self._page.add(self._dns)
        self.add_named(self._page, "list")
        follow(self, [model.changed], self.update)

    def update(self) -> None:
        profile = self._model.store.profile(self._profile_id)
        if profile is None:
            return
        routes = RouteRow.rows(profile, self._model.profile_name, self._model.presenter)
        dns = DnsRow.rows(profile, self._strings)
        declared = tuple(profile.summary.dns_servers)
        if not routes and not dns and not declared:
            self.set_visible_child_name("empty")
            return
        self.set_visible_child_name("list")

        shown = (tuple(routes), tuple(dns), declared)
        if shown == self._shown:
            return
        self._shown = shown
        for group, row in self._rows:
            group.remove(row)
        self._rows.clear()

        for route in routes:
            row = Adw.ActionRow(title=_mono(route.prefix), title_selectable=True, subtitle_lines=0)
            if route.detail:
                row.set_subtitle(GLib.markup_escape_text(route.detail))
            if route.state is not None:
                row.add_suffix(StatusLabel(route.state, route.state_label))
            self._add(self._routes, row)
        self._routes.set_visible(bool(routes))

        # The servers the profile names are shown while it is not connected.
        if not dns and declared:
            row = Adw.ActionRow(title=_mono(", ".join(declared)), title_selectable=True)
            self._add(self._dns, row)
        for entry in dns:
            row = Adw.ActionRow(
                title=_mono(entry.servers),
                subtitle=GLib.markup_escape_text(" · ".join(part for part in (entry.domains, entry.detail) if part)),
                title_selectable=True,
                subtitle_lines=0,
            )
            row.add_suffix(StatusLabel(entry.state, self._model.presenter.route_state_label(entry.state)))
            self._add(self._dns, row)
        self._dns.set_visible(bool(dns) or bool(declared))

    def _add(self, group: Adw.PreferencesGroup, row: Adw.ActionRow) -> None:
        group.add(row)
        self._rows.append((group, row))
