"""The app's own settings and the helper: the Settings page of the window, which
Settings… (Ctrl+,) opens."""

from __future__ import annotations

import gi

gi.require_version("Gtk", "4.0")
gi.require_version("Adw", "1")
from gi.repository import Adw, GLib, Gtk  # noqa: E402

from ...client.types import EngineInfo  # noqa: E402
from ...core.app_model import AppModel  # noqa: E402
from ...core.ports import HelperRegistration  # noqa: E402
from ..widgets import follow, text_button  # noqa: E402


class AppSettingsPage(Adw.PreferencesPage):
    def __init__(self, model: AppModel) -> None:
        super().__init__()
        self._model = model
        self._strings = model.strings
        self._updating = False
        self._info_rows: list[Adw.ActionRow] = []

        s = self._strings
        general = Adw.PreferencesGroup()
        self._launch = Adw.SwitchRow(title=s.launch_at_login)
        self._launch.connect("notify::active", self._launch_changed)
        self._tray = Adw.SwitchRow(title=s.show_tray_icon)
        self._tray.connect("notify::active", self._tray_changed)
        general.add(self._launch)
        general.add(self._tray)
        self.add(general)

        self._helper = Adw.PreferencesGroup(title=s.helper)
        self.add(self._helper)
        self._restart_row = Adw.ActionRow(title=s.restart_helper)
        self._restart = text_button(s.restart_helper, model.request_restart_helper)
        self._restart_row.add_suffix(self._restart)
        self._restart_row.set_activatable_widget(self._restart)
        self._helper.add(self._restart_row)

        self.connect("map", lambda *_: model.run_job(model.refresh_helper))
        follow(self, [model.changed], self.update)

    def update(self) -> None:
        model = self._model
        s = self._strings
        self._updating = True
        try:
            self._launch.set_active(model.launches_at_login)
            self._tray.set_active(model.show_tray_icon)
        finally:
            self._updating = False

        for row in self._info_rows:
            self._helper.remove(row)
        self._info_rows.clear()
        rows: list[tuple[str, str]] = []
        registration = model.registration
        if not model.is_overridden and registration is not None:
            rows.append((s.status, self._registration_text(registration)))
        info = model.store.daemon_info
        if info is not None:
            rows.append((s.helper_version, info.version))
            for engine in info.engines:
                rows.append((model.presenter.kind_text(engine.kind), self._engine_text(engine)))
            if not info.privileged:
                rows.append((s.privileges, s.limited))
        if model.app_version:
            rows.append((s.app_version, model.app_version))
        for title, value in rows:
            row = Adw.ActionRow(title=title, subtitle=GLib.markup_escape_text(value), subtitle_selectable=True, subtitle_lines=0)
            row.add_css_class("property")
            self._helper.add(row)
            self._info_rows.append(row)
        # Restarting is for a helper that is a unit of this system's systemd.
        self._restart_row.set_visible(model.can_manage_helper)

    def _registration_text(self, registration: HelperRegistration) -> str:
        s = self._strings
        match registration:
            case HelperRegistration.RUNNING:
                return s.running
            case HelperRegistration.STOPPED:
                return s.stopped
            case HelperRegistration.NOT_INSTALLED:
                return s.not_installed
            case _:
                return s.unavailable

    def _engine_text(self, engine: EngineInfo) -> str:
        """The engine's version, or why it cannot run."""
        if engine.available:
            return engine.version
        return engine.detail or self._strings.unavailable

    def _launch_changed(self, row: Adw.SwitchRow, _) -> None:
        if not self._updating:
            self._model.set_launch_at_login(row.get_active())

    def _tray_changed(self, row: Adw.SwitchRow, _) -> None:
        if not self._updating:
            self._model.set_show_tray_icon(row.get_active())
