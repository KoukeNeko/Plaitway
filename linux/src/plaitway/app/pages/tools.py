"""The Tools page: what comes with the app and is not the app, in categories. GNOME Shell extensions are the first:
the top bar item, which this page installs and enables."""

from __future__ import annotations

import gi

gi.require_version("Gtk", "4.0")
gi.require_version("Adw", "1")
from gi.repository import Adw, Gtk  # noqa: E402

from ...core.app_model import AppModel  # noqa: E402
from ...core.gnome_extension import ExtensionState, ExtensionStatus  # noqa: E402
from .. import gnome_extension  # noqa: E402
from ..widgets import label, text_button  # noqa: E402


class ToolsPage(Adw.PreferencesPage):
    def __init__(self, model: AppModel) -> None:
        super().__init__()
        self._model = model
        s = model.strings

        self._extensions = Adw.PreferencesGroup(title=s.gnome_shell_extensions, description=s.extensions_description)
        self.add(self._extensions)
        self._row = Adw.ActionRow(title=s.top_bar_item, subtitle=s.top_bar_item_description, subtitle_lines=0)
        self._status = label(css=["dimmed"])
        self._status.set_valign(Gtk.Align.CENTER)
        self._enable = text_button(s.enable, lambda: self._change(lambda: gnome_extension.set_enabled(True)))
        self._disable = text_button(s.disable, lambda: self._change(lambda: gnome_extension.set_enabled(False)))
        self._install = text_button(s.install, lambda: self._change(gnome_extension.install), css=["suggested-action"])
        self._uninstall = text_button(s.uninstall, lambda: self._change(gnome_extension.uninstall))
        for widget in (self._status, self._uninstall, self._disable, self._enable, self._install):
            self._row.add_suffix(widget)
        self._extensions.add(self._row)

        self._notice = Adw.ActionRow(title=s.log_out_to_load)
        self._notice.add_prefix(Gtk.Image(icon_name="dialog-information-symbolic", accessible_role=Gtk.AccessibleRole.PRESENTATION))
        self._notice.set_visible(False)
        self._extensions.add(self._notice)

        self._shown = ExtensionStatus(ExtensionState.UNAVAILABLE)
        self._show(self._shown)
        self.connect("map", lambda *_: self._refresh())

    def _refresh(self) -> None:
        self._model.run_job(gnome_extension.inspect, self._inspected)

    def _inspected(self, outcome) -> None:
        if outcome.ok:
            self._show(outcome.value)

    def _change(self, change) -> None:
        """A change of the extension, then what the machine says about it."""

        self._model.change_extension(change, self._refresh)

    def _show(self, status: ExtensionStatus) -> None:
        s = self._model.strings
        state = status.state
        words = {
            ExtensionState.UNAVAILABLE: s.requires_gnome_shell,
            ExtensionState.NOT_INSTALLED: s.not_installed,
            ExtensionState.DISABLED: s.disabled,
            ExtensionState.ENABLED: s.enabled,
        }[state]
        self._status.set_text(words)
        self._install.set_visible(state is ExtensionState.NOT_INSTALLED)
        self._enable.set_visible(state is ExtensionState.DISABLED)
        self._disable.set_visible(state is ExtensionState.ENABLED)
        self._uninstall.set_visible(status.user_installed)
        self._notice.set_visible(status.needs_login)
        self._row.set_sensitive(state is not ExtensionState.UNAVAILABLE)
