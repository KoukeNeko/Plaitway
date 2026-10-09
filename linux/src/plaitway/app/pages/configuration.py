"""The profile's own text, to read and to change: the file the daemon keeps for it,
with the secrets hidden until asked for."""

from __future__ import annotations

import gi

gi.require_version("Gtk", "4.0")
gi.require_version("Adw", "1")
from gi.repository import Adw, Gio, Gtk  # noqa: E402

from ...core.app_model import AlertTitle, AppModel  # noqa: E402
from ...core.presentation import Tone  # noqa: E402
from ...core.profile_editor import Phase, SaveResult  # noqa: E402
from ..config_editor import ConfigEditor  # noqa: E402
from ..widgets import NoticeBar, follow, label, page_bar, spinner, status_page  # noqa: E402


class ConfigurationPage(Gtk.Box):
    def __init__(self, model: AppModel, profile_id: str) -> None:
        super().__init__(orientation=Gtk.Orientation.VERTICAL)
        self._model = model
        self._profile_id = profile_id
        self._strings = model.strings
        profile = model.store.profile(profile_id)
        self._editor = model.editor(profile)
        self._connected_since: tuple | None = None

        self._build_bar()
        self._build_notices()
        self._build_body(profile.kind)
        self._build_shortcuts()

        self.connect("map", self._appear)
        self.connect("unmap", lambda *_: self._editor.hide_secrets())
        follow(self, [self._editor.changed, model.changed], self.update)

    # MARK: Building

    def _build_bar(self) -> None:
        s = self._strings
        bar = page_bar()
        self._revert = Gtk.Button(label=s.revert)
        self._revert.connect("clicked", lambda _: self._editor.revert())
        bar.append(self._revert)

        self._secrets = Gtk.ToggleButton()
        self._secrets_content = Adw.ButtonContent(label=s.show_secrets, icon_name="view-reveal-symbolic")
        self._secrets.set_child(self._secrets_content)
        self._secrets.connect("clicked", lambda _: self._editor.toggle_secrets())
        bar.append(self._secrets)
        bar.append(Gtk.Box(hexpand=True))

        # A running profile keeps the text it started with: the person says whether to restart it.
        self._save = Gtk.Button(label=s.save)
        self._save.add_css_class("suggested-action")
        self._save.connect("clicked", lambda _: self._start_save(reconnect=False))
        bar.append(self._save)
        menu = Gio.Menu()
        menu.append(s.save_and_reconnect, "config.save-and-reconnect")
        self._save_split = Adw.SplitButton(label=s.save, menu_model=menu)
        self._save_split.add_css_class("suggested-action")
        self._save_split.connect("clicked", lambda _: self._start_save(reconnect=False))
        bar.append(self._save_split)
        actions = Gio.SimpleActionGroup()
        reconnect = Gio.SimpleAction.new("save-and-reconnect", None)
        reconnect.connect("activate", lambda *_: self._start_save(reconnect=True))
        actions.add_action(reconnect)
        self._reconnect_action = reconnect
        self.insert_action_group("config", actions)
        self.append(bar)

    def _build_notices(self) -> None:
        self._notices = Gtk.Box(orientation=Gtk.Orientation.VERTICAL)
        self._diagnostic = NoticeBar(Tone.ERROR, "dialog-error-symbolic")
        self._warnings = NoticeBar(Tone.WARNING, "dialog-warning-symbolic")
        self._applies_later = NoticeBar(Tone.NORMAL, "dialog-information-symbolic")
        self._applies_later.set_messages([label(self._strings.applies_at_next_connection)])
        for notice in (self._diagnostic, self._warnings, self._applies_later):
            self._notices.append(notice)
        self.append(self._notices)

    def _build_body(self, kind: int) -> None:
        self._stack = Gtk.Stack(vexpand=True)
        self._stack.add_named(spinner(halign=Gtk.Align.CENTER, valign=Gtk.Align.CENTER, width_request=32, height_request=32), "loading")
        self._unavailable = status_page("changes-prevent-symbolic", "")
        self._stack.add_named(self._unavailable, "unavailable")
        self._editor_widget = ConfigEditor(kind, self._edited, self._strings.configuration)
        self._stack.add_named(self._editor_widget, "ready")
        self.append(self._stack)

    def _build_shortcuts(self) -> None:
        controller = Gtk.ShortcutController()
        controller.set_scope(Gtk.ShortcutScope.LOCAL)
        controller.add_shortcut(Gtk.Shortcut.new(
            Gtk.ShortcutTrigger.parse_string("<Control>s"),
            Gtk.CallbackAction.new(lambda *_: self._save_shortcut()),
        ))
        self.add_controller(controller)

    # MARK: Model to view

    def _appear(self, *_) -> None:
        self._editor.load()

    def update(self) -> None:
        profile = self._model.store.profile(self._profile_id)
        if profile is None:
            return
        editor = self._editor
        since = (profile.status.connected_since.seconds, profile.status.connected_since.nanos)
        if self._connected_since is not None and since != self._connected_since:
            editor.note_restart()
        self._connected_since = since

        self._stack.set_visible_child_name(
            {Phase.LOADING: "loading", Phase.UNAVAILABLE: "unavailable", Phase.READY: "ready"}[editor.phase]
        )
        self._unavailable.set_title(editor.unavailable_message)
        ready = editor.phase is Phase.READY
        if ready:
            self._editor_widget.set_text(editor.text)
            self._editor_widget.set_marked_line(editor.diagnostic.line if editor.diagnostic else None)
            self._editor_widget.set_editable(not editor.is_saving)
        self._update_notices()
        self._update_buttons(profile.desired_enabled, ready)

    def _update_buttons(self, running: bool, ready: bool) -> None:
        s = self._strings
        editor = self._editor
        can_save = editor.is_dirty and not editor.is_saving
        self._revert.set_sensitive(editor.is_dirty)
        self._secrets.set_sensitive(ready)
        label = s.hide_secrets if editor.shows_secrets else s.show_secrets
        # A narrow window has room for the icon only; the label is the tooltip.
        compact = self._model.compact
        self._secrets_content.set_label("" if compact else label)
        self._secrets.set_tooltip_text(label if compact else None)
        self._secrets_content.set_icon_name("view-conceal-symbolic" if editor.shows_secrets else "view-reveal-symbolic")
        self._secrets.set_active(editor.shows_secrets)
        self._save.set_visible(not running)
        self._save_split.set_visible(running)
        self._save.set_sensitive(can_save)
        self._save_split.set_sensitive(can_save)
        self._reconnect_action.set_enabled(can_save)

    def _update_notices(self) -> None:
        editor = self._editor
        diagnostic = editor.diagnostic
        self._diagnostic.set_visible(diagnostic is not None)
        if diagnostic is not None:
            parts = []
            if diagnostic.line is not None:
                parts.append(label(self._strings.line_number(diagnostic.line), css=["numeric"]))
            parts.append(label(diagnostic.message, selectable=True, wrap=True))
            self._diagnostic.set_messages(parts)
        show_warnings = bool(editor.warnings) and not editor.is_dirty
        self._warnings.set_visible(show_warnings)
        if show_warnings:
            summaries = [self._model.presenter.warning_summary(w) for w in editor.warnings]
            column = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=2)
            for summary in summaries:
                column.append(label(summary, selectable=True, wrap=True))
            self._warnings.set_messages([column])
        self._applies_later.set_visible(editor.runs_old_text and not editor.is_dirty)

    # MARK: View to model

    def _edited(self, text: str) -> None:
        self._editor.set_text(text)
        profile = self._model.store.profile(self._profile_id)
        self._update_buttons(bool(profile and profile.desired_enabled), True)

    def _save_shortcut(self) -> bool:
        if self._editor.is_dirty and not self._editor.is_saving:
            self._start_save(reconnect=False)
        return True

    def _start_save(self, *, reconnect: bool) -> None:
        profile = self._model.store.profile(self._profile_id)
        if profile is None:
            return

        def finished(result: SaveResult) -> None:
            if result.error is not None:
                self._model.report(result.error, AlertTitle.SAVE_FAILED)

        self._editor.save(reconnect, profile.desired_enabled, finished)
