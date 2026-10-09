"""Diagnostics: the network, the routes the helper owns, what is stale, and the
helper's own log."""

from __future__ import annotations

import gi

gi.require_version("Gtk", "4.0")
gi.require_version("Adw", "1")
from gi.repository import Adw, GLib, Gtk  # noqa: E402

from ...client.types import StaleRoute  # noqa: E402
from ...core import formatting  # noqa: E402
from ...core.app_model import AppModel, DiagnosticsPage  # noqa: E402
from ...core.diagnostics_model import DiagnosticsModel  # noqa: E402
from ..widgets import StatusLabel, follow, label, page_bar, spinner, text_button  # noqa: E402
from .icons import ICONS
from .logs import LogsPage

def build_diagnostics_pages(model: AppModel) -> Adw.ViewStack:
    stack = Adw.ViewStack(hhomogeneous=False)
    strings = model.strings
    pages = {
        DiagnosticsPage.OVERVIEW: DiagnosticsOverview(model),
        # An empty id is the helper's own log.
        DiagnosticsPage.HELPER_LOG: LogsPage(model, ""),
    }
    for page, widget in pages.items():
        stack.add_titled_with_icon(widget, page.name.lower(), page.label(strings), ICONS[page.name.lower()])
    return stack


class DiagnosticsOverview(Gtk.Box):
    def __init__(self, model: AppModel) -> None:
        super().__init__(orientation=Gtk.Orientation.VERTICAL)
        self._model = model
        self._strings = model.strings
        self._diagnostics = DiagnosticsModel(model.store, model.strings, model.platform.scheduler)
        self._shown = None
        self._resyncing = False
        self._groups: list[Adw.PreferencesGroup] = []
        self._rows: list[tuple[Adw.PreferencesGroup, Gtk.Widget]] = []

        s = self._strings
        bar = page_bar()
        bar.append(Gtk.Box(hexpand=True))
        self._copy = text_button(s.copy_report, self._copy_report)
        self._resync = text_button(s.resync, self._resync_now)
        bar.append(self._copy)
        bar.append(self._resync)
        self.append(bar)

        self._stack = Gtk.Stack(vexpand=True)
        self._waiting = spinner(halign=Gtk.Align.CENTER, valign=Gtk.Align.CENTER, width_request=32, height_request=32)
        self._stack.add_named(self._waiting, "waiting")
        self._failure = Adw.StatusPage(icon_name="dialog-warning-symbolic")
        self._stack.add_named(self._failure, "failure")
        self._page = Adw.PreferencesPage()
        self._stack.add_named(self._page, "overview")
        self.append(self._stack)

        # The reading is polled only while it is on screen: on the helper log page every
        # GetDiagnostics call would show up in the log being read.
        self.connect("map", lambda *_: self._diagnostics.start())
        self.connect("unmap", lambda *_: self._diagnostics.stop())
        follow(self, [self._diagnostics.changed, model.changed], self.update)

    def update(self) -> None:
        current = self._diagnostics.diagnostics
        self._copy.set_sensitive(current is not None)
        self._resync.set_sensitive(not self._resyncing)
        if current is None:
            failure = self._diagnostics.failure
            self._failure.set_title(failure or "")
            self._stack.set_visible_child_name("failure" if failure else "waiting")
            return
        self._stack.set_visible_child_name("overview")
        names = {profile.id: profile.name for profile in self._model.store.profiles}
        key = (current.SerializeToString(), tuple(sorted(names.items())))
        if key == self._shown:
            return
        self._shown = key
        self._rebuild(current, names)

    def _rebuild(self, diagnostics, names: dict[str, str]) -> None:
        s = self._strings
        presenter = self._model.presenter
        for group in self._groups:
            self._page.remove(group)
        self._groups.clear()

        def named(profile_id: str) -> str:
            return names.get(profile_id) or profile_id

        # What needs doing comes first: the product exists to leave none of these behind.
        if diagnostics.stale_routes:
            group = self._group(s.stale_routes)
            for route in diagnostics.stale_routes:
                group.add(self._stale_row(route))

        network = diagnostics.network
        group = self._group(s.network)
        for title, value in [
            (s.default_gateway, " · ".join(p for p in (network.default_gateway_v4, network.default_interface_v4) if p)),
            (s.default_gateway_ipv6, " · ".join(p for p in (network.default_gateway_v6, network.default_interface_v6) if p)),
            (s.interfaces, ", ".join(network.interfaces)),
            (
                s.last_change,
                " · ".join(
                    p for p in (
                        formatting.format_log_time(network.last_change.seconds) if network.HasField("last_change") else "",
                        network.last_change_reason,
                    ) if p
                ),
            ),
        ]:
            if value:
                group.add(_property_row(title, value))

        group = self._group(s.routes)
        if not diagnostics.owned_routes:
            group.add(Adw.ActionRow(title=s.none, css_classes=["dimmed"]))
        for route in diagnostics.owned_routes:
            row = Adw.ActionRow(
                title=f"<tt>{GLib.markup_escape_text(route.prefix)}</tt>",
                subtitle=GLib.markup_escape_text(
                    f"{named(route.owner)} · {presenter.route_kind_label(route.kind)} · {route.via}"
                ),
                title_selectable=True,
            )
            row.add_suffix(StatusLabel(route.state, presenter.route_state_label(route.state)))
            group.add(row)

        group = self._group(None)
        group.add(self._expander(s.resolver_entries, [
            Adw.ActionRow(title=f"<tt>{GLib.markup_escape_text(entry)}</tt>", title_selectable=True)
            for entry in diagnostics.resolver_entries
        ]))

        group = self._group(None)
        entries = []
        # Newest first.
        for entry in list(reversed(diagnostics.recent_journal))[:50]:
            when = formatting.format_log_time(entry.time.seconds) if entry.HasField("time") else ""
            caption = " · ".join(p for p in (when, named(entry.owner) if entry.owner else "") if p)
            row = Adw.ActionRow(
                title=f"<tt>{GLib.markup_escape_text(f'{entry.kind} {entry.key}')}</tt>",
                subtitle=GLib.markup_escape_text(caption),
            )
            row.add_suffix(label(entry.state, css=["dimmed"]))
            entries.append(row)
        group.add(self._expander(s.recent_changes, entries))

    def _group(self, title: str | None) -> Adw.PreferencesGroup:
        group = Adw.PreferencesGroup(title=title) if title else Adw.PreferencesGroup()
        self._page.add(group)
        self._groups.append(group)
        return group

    def _expander(self, title: str, rows: list[Gtk.Widget]) -> Adw.ExpanderRow:
        expander = Adw.ExpanderRow(title=title)
        if not rows:
            expander.add_row(Adw.ActionRow(title=self._strings.none, css_classes=["dimmed"]))
        for row in rows:
            expander.add_row(row)
        return expander

    def _stale_row(self, route: StaleRoute) -> Adw.ActionRow:
        s = self._strings
        details = [
            " · ".join(p for p in (route.gateway, route.interface) if p),
            route.reason,
            s.installed_by_plaitway if route.owned else "",
        ]
        row = Adw.ActionRow(
            title=f"<tt>{GLib.markup_escape_text(route.prefix)}</tt>",
            subtitle=GLib.markup_escape_text("\n".join(d for d in details if d)),
            subtitle_lines=0,
        )
        icon = Gtk.Image(icon_name="dialog-warning-symbolic", accessible_role=Gtk.AccessibleRole.PRESENTATION)
        icon.add_css_class("warning")
        row.add_prefix(icon)
        row.add_suffix(text_button(s.remove, lambda: self._remove(route)))
        return row

    # MARK: Commands

    def _remove(self, route: StaleRoute) -> None:
        self._model.request_remove_stale_route(route, lambda: self._refresh_soon())

    def _refresh_soon(self) -> None:
        self._model.run_job(self._diagnostics.refresh)

    def _resync_now(self) -> None:
        self._resyncing = True
        self.update()

        def finished() -> None:
            self._resyncing = False
            self.update()
            self._refresh_soon()

        self._model.resync(finished)

    def _copy_report(self) -> None:
        current = self._diagnostics.diagnostics
        if current is not None:
            self._model.copy_report(current)


def _property_row(title: str, value: str) -> Adw.ActionRow:
    row = Adw.ActionRow(title=title, subtitle=GLib.markup_escape_text(value), subtitle_selectable=True, subtitle_lines=0)
    row.add_css_class("property")
    return row
