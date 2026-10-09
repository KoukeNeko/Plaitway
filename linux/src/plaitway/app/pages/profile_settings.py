"""How one profile behaves: its name, when it connects, and what it may take over."""

from __future__ import annotations

import gi

gi.require_version("Gtk", "4.0")
gi.require_version("Adw", "1")
from gi.repository import Adw, Gtk  # noqa: E402

from ...client.types import ProfileKind, ProfileState, TunnelMode  # noqa: E402
from ...core.app_model import AppModel  # noqa: E402
from ...core.presentation import TUNNEL_MODE_CHOICES  # noqa: E402
from ..widgets import follow, icon_button, label  # noqa: E402


class ProfileSettingsPage(Adw.PreferencesPage):
    def __init__(self, model: AppModel, profile_id: str) -> None:
        super().__init__()
        self._model = model
        self._profile_id = profile_id
        self._strings = model.strings
        self._updating = False
        self._name_focused = False
        self._has_saved_credentials = False

        self._build_behaviour()
        self._build_on_demand()
        self._build_actions()
        follow(self, [model.changed], self.update)
        self.connect("map", lambda *_: self._look_for_credentials())

    # MARK: Building

    def _build_behaviour(self) -> None:
        s = self._strings
        group = Adw.PreferencesGroup()

        self._name = Adw.EntryRow(title=s.name, show_apply_button=True)
        self._name.connect("apply", lambda *_: self._commit_name())
        focus = Gtk.EventControllerFocus()
        focus.connect("enter", lambda *_: setattr(self, "_name_focused", True))
        focus.connect("leave", self._name_left)
        self._name.add_controller(focus)
        group.add(self._name)

        self._auto_connect = Adw.SwitchRow(title=s.auto_connect)
        self._auto_connect.connect("notify::active", self._auto_connect_changed)
        group.add(self._auto_connect)

        self._tunnel_mode = Adw.ComboRow(
            title=s.tunnel_mode, model=Gtk.StringList.new([self._model.presenter.tunnel_mode_label(m) for m in TUNNEL_MODE_CHOICES])
        )
        self._tunnel_mode.connect("notify::selected", self._tunnel_mode_changed)
        group.add(self._tunnel_mode)

        self._exclude_private = Adw.SwitchRow(title=s.exclude_private_ips)
        self._exclude_private.connect("notify::active", self._exclude_private_changed)
        group.add(self._exclude_private)

        self._priority = Adw.ActionRow(title=s.priority, tooltip_text=s.priority_hint)
        self._position = label(css=["numeric"])
        self._priority.add_suffix(self._position)
        self._up = icon_button("go-up-symbolic", s.move_up, lambda: self._model.move_by(self._profile_id, -1))
        self._down = icon_button("go-down-symbolic", s.move_down, lambda: self._model.move_by(self._profile_id, 1))
        self._priority.add_suffix(self._up)
        self._priority.add_suffix(self._down)
        group.add(self._priority)
        self.add(group)

    def _build_on_demand(self) -> None:
        s = self._strings
        group = Adw.PreferencesGroup(title=s.on_demand, description=s.on_demand_footer)
        self._ethernet = Adw.SwitchRow(title=s.ethernet)
        self._ethernet.connect("notify::active", lambda row, _: self._on_demand_changed("ethernet", row))
        self._wifi = Adw.SwitchRow(title=s.wi_fi)
        self._wifi.connect("notify::active", lambda row, _: self._on_demand_changed("wifi", row))
        group.add(self._ethernet)
        group.add(self._wifi)
        self.add(group)

    def _build_actions(self) -> None:
        s = self._strings
        group = Adw.PreferencesGroup()
        self._forget_row = Adw.ActionRow(title=s.remove_saved_credentials, activatable=True)
        self._forget_row.connect("activated", lambda *_: self._forget_credentials())
        group.add(self._forget_row)
        delete = Adw.ActionRow(title=s.delete_profile, activatable=True)
        delete.add_css_class("error")
        delete.connect("activated", lambda *_: self._model.request_deletion(self._profile_id))
        group.add(delete)
        self.add(group)

    # MARK: Model to view

    def update(self) -> None:
        profile = self._model.store.profile(self._profile_id)
        if profile is None:
            return
        s = self._strings
        settings = profile.settings
        self._updating = True
        try:
            if not self._name_focused and self._name.get_text() != profile.name:
                self._name.set_text(profile.name)
            self._auto_connect.set_active(settings.auto_connect)
            mode = TunnelMode(settings.tunnel_mode)
            self._tunnel_mode.set_selected(TUNNEL_MODE_CHOICES.index(mode) if mode in TUNNEL_MODE_CHOICES else 0)
            self._ethernet.set_active(settings.on_demand.ethernet)
            self._wifi.set_active(settings.on_demand.wifi)
            self._exclude_private.set_active(settings.exclude_private_ips)
        finally:
            self._updating = False

        idle = profile.state in (ProfileState.DISCONNECTED, ProfileState.FAILED)
        # The tunnel mode and the address ranges are read when a connection starts.
        later = "" if idle else s.applies_at_next_connection
        self._tunnel_mode.set_subtitle(later)
        self._exclude_private.set_visible(profile.kind == ProfileKind.WIREGUARD)
        self._exclude_private.set_subtitle(s.private_ranges_hint if idle else later)

        position = self._model.store.profiles.ids.index(self._profile_id) + 1
        count = len(self._model.store.profiles)
        self._position.set_text(str(position))
        self._up.set_sensitive(position > 1)
        self._down.set_sensitive(position < count)
        self._forget_row.set_visible(self._has_saved_credentials)

    def _look_for_credentials(self) -> None:
        def found(has: bool) -> None:
            if has != self._has_saved_credentials:
                self._has_saved_credentials = has
                self.update()

        self._model.check_saved_credentials(self._profile_id, found)

    # MARK: View to model

    def _name_left(self, *_) -> None:
        self._name_focused = False
        self._commit_name()

    def _commit_name(self) -> None:
        name = self._name.get_text()

        def finished(kept: bool) -> None:
            # The daemon may refuse the name (another profile has it); the field then
            # goes back to the name the profile still has.
            if not kept:
                profile = self._model.store.profile(self._profile_id)
                if profile is not None:
                    self._name.set_text(profile.name)

        self._model.rename(self._profile_id, name, finished)

    def _auto_connect_changed(self, row: Adw.SwitchRow, _) -> None:
        if not self._updating:
            active = row.get_active()
            self._model.change_settings(self._profile_id, lambda settings: setattr(settings, "auto_connect", active))

    def _tunnel_mode_changed(self, row: Adw.ComboRow, _) -> None:
        if not self._updating:
            mode = TUNNEL_MODE_CHOICES[row.get_selected()]
            self._model.change_settings(self._profile_id, lambda settings: setattr(settings, "tunnel_mode", int(mode)))

    def _exclude_private_changed(self, row: Adw.SwitchRow, _) -> None:
        if not self._updating:
            active = row.get_active()
            self._model.change_settings(self._profile_id, lambda settings: setattr(settings, "exclude_private_ips", active))

    def _on_demand_changed(self, network: str, row: Adw.SwitchRow) -> None:
        if not self._updating:
            active = row.get_active()
            self._model.change_settings(self._profile_id, lambda settings: setattr(settings.on_demand, network, active))

    def _forget_credentials(self) -> None:
        def done() -> None:
            self._has_saved_credentials = False
            self.update()

        self._model.forget_saved_credentials(self._profile_id, done)
