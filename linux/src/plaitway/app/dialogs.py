"""The questions and reports of the app, as libadwaita dialogs. Every method may be
called from any thread; the dialog is made on the UI thread."""

from __future__ import annotations

from collections.abc import Callable

import gi

gi.require_version("Gtk", "4.0")
gi.require_version("Adw", "1")
from gi.repository import Adw, GLib, Gtk  # noqa: E402

from ..client.types import CredentialKind  # noqa: E402
from ..core.import_report import ImportReport  # noqa: E402
from ..core.ports import Confirmation, CredentialPrompt, QuitAnswer, UiScheduler  # noqa: E402
from ..core.presentation import Presenter  # noqa: E402
from ..l10n.strings import Strings  # noqa: E402
from .widgets import label  # noqa: E402


class GtkDialogs:
    def __init__(self, strings: Strings, scheduler: UiScheduler, parent: Callable[[], Gtk.Window | None]) -> None:
        self._strings = strings
        self._scheduler = scheduler
        self._parent = parent
        self._presenter = Presenter(strings)
        self._credential_dialogs: dict[str, Adw.AlertDialog] = {}

    def _later(self, fn: Callable[[], None]) -> None:
        self._scheduler.post(fn)

    # MARK: Alerts and questions

    def show_alert(self, title: str, message: str) -> None:
        def show() -> None:
            dialog = Adw.AlertDialog(heading=title, body=message)
            dialog.add_response("close", self._strings.close)
            dialog.set_default_response("close")
            dialog.set_close_response("close")
            dialog.present(self._parent())

        self._later(show)

    def confirm(self, confirmation: Confirmation, on_answer: Callable[[bool], None]) -> None:
        def show() -> None:
            dialog = Adw.AlertDialog(heading=confirmation.title, body=confirmation.message)
            dialog.add_response("cancel", self._strings.cancel)
            dialog.add_response("confirm", confirmation.action)
            dialog.set_response_appearance(
                "confirm", Adw.ResponseAppearance.DESTRUCTIVE if confirmation.destructive else Adw.ResponseAppearance.SUGGESTED
            )
            dialog.set_default_response("cancel")
            dialog.set_close_response("cancel")
            dialog.connect("response", lambda _, response: on_answer(response == "confirm"))
            dialog.present(self._parent())

        self._later(show)

    # MARK: Import

    def show_import_report(self, report: ImportReport) -> None:
        self._later(lambda: self._show_import_report(report))

    def _show_import_report(self, report: ImportReport) -> None:
        s = self._strings
        dialog = Adw.Dialog(title=s.import_label, content_width=520, content_height=360)
        view = Adw.ToolbarView()
        view.add_top_bar(Adw.HeaderBar())
        listbox = Gtk.ListBox(
            selection_mode=Gtk.SelectionMode.NONE, valign=Gtk.Align.START,
            margin_start=12, margin_end=12, margin_top=12, margin_bottom=12,
        )
        listbox.add_css_class("boxed-list")
        for outcome in report.outcomes:
            listbox.append(self._outcome_row(outcome))
        scrolled = Gtk.ScrolledWindow(child=listbox, vexpand=True, hscrollbar_policy=Gtk.PolicyType.NEVER)
        view.set_content(scrolled)
        close = Gtk.Button(label=s.close, halign=Gtk.Align.END, margin_start=12, margin_end=12, margin_bottom=12)
        close.add_css_class("suggested-action")
        close.connect("clicked", lambda _: dialog.close())
        view.add_bottom_bar(close)
        dialog.set_child(view)
        dialog.set_default_widget(close)
        dialog.present(self._parent())

    def _outcome_row(self, outcome) -> Adw.ActionRow:
        lines: list[str] = []
        if outcome.imported:
            lines.append(outcome.profile_name)
            lines += [self._presenter.warning_summary(w) for w in outcome.warnings]
            icon, tone = ("dialog-warning-symbolic", "warning") if outcome.warnings else ("object-select-symbolic", "success")
        else:
            lines.append(outcome.failure)
            icon, tone = "dialog-error-symbolic", "error"
        row = Adw.ActionRow(
            title=GLib.markup_escape_text(outcome.filename),
            subtitle=GLib.markup_escape_text("\n".join(lines)),
            subtitle_lines=0,
            subtitle_selectable=True,
        )
        glyph = Gtk.Image(icon_name=icon, accessible_role=Gtk.AccessibleRole.PRESENTATION)
        glyph.add_css_class(tone)
        row.add_prefix(glyph)
        return row

    # MARK: Credentials

    def ask_credentials(
        self,
        prompt: CredentialPrompt,
        profile_name: str,
        on_submit: Callable[[str, str], None],
        on_cancel: Callable[[], None],
    ) -> None:
        self._later(lambda: self._ask_credentials(prompt, profile_name, on_submit, on_cancel))

    def dismiss_credentials(self, profile_id: str) -> None:
        def dismiss() -> None:
            dialog = self._credential_dialogs.pop(profile_id, None)
            if dialog is not None:
                dialog.force_close()

        self._later(dismiss)

    def _ask_credentials(self, prompt, profile_name, on_submit, on_cancel) -> None:
        s = self._strings
        asks_for_username = prompt.kind != CredentialKind.KEY_PASSPHRASE
        dialog = Adw.AlertDialog(
            heading=s.credentials if asks_for_username else s.key_passphrase, body=profile_name
        )
        dialog.add_response("cancel", s.cancel)
        dialog.add_response("connect", s.connect)
        dialog.set_response_appearance("connect", Adw.ResponseAppearance.SUGGESTED)
        dialog.set_default_response("connect")
        dialog.set_close_response("cancel")
        dialog.set_response_enabled("connect", False)

        child = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=12)
        if prompt.rejected:
            child.append(label(prompt.message or s.credentials_rejected, css=["error"], wrap=True))
        fields = Gtk.ListBox(selection_mode=Gtk.SelectionMode.NONE)
        fields.add_css_class("boxed-list")
        username = Adw.EntryRow(title=s.username)
        password = Adw.PasswordEntryRow(title=s.password if asks_for_username else s.passphrase)
        if asks_for_username:
            fields.append(username)
        fields.append(password)
        child.append(fields)
        dialog.set_extra_child(child)

        answered = False

        def can_submit() -> bool:
            return bool(password.get_text()) and (not asks_for_username or bool(username.get_text()))

        def answer(submit: bool) -> None:
            nonlocal answered
            if answered:
                return
            answered = True
            self._credential_dialogs.pop(prompt.profile_id, None)
            if submit:
                secret = password.get_text()
                # The dialog goes away with the prompt; its fields do not keep the secret.
                password.set_text("")
                on_submit(username.get_text() if asks_for_username else "", secret)
            else:
                on_cancel()
            dialog.force_close()

        def changed(*_) -> None:
            dialog.set_response_enabled("connect", can_submit())

        def activated(*_) -> None:
            if can_submit():
                answer(True)

        username.connect("changed", changed)
        password.connect("changed", changed)
        username.connect("entry-activated", lambda *_: password.grab_focus())
        password.connect("entry-activated", activated)
        dialog.connect("response", lambda _, response: answer(response == "connect"))
        self._credential_dialogs[prompt.profile_id] = dialog
        dialog.present(self._parent())
        (username if asks_for_username else password).grab_focus()


class GtkQuitPrompts:
    """What Quit asks while profiles are switched on: the helper, not the app, keeps them connected."""

    def __init__(self, strings: Strings, scheduler: UiScheduler, parent: Callable[[], Gtk.Window | None]) -> None:
        self._strings = strings
        self._scheduler = scheduler
        self._parent = parent

    def ask_quit(self, on_answer: Callable[[QuitAnswer], None]) -> None:
        s = self._strings

        def show() -> None:
            dialog = Adw.AlertDialog(heading=s.quit_plaitway_title, body=s.connected_profiles_stay_connected)
            dialog.add_response("cancel", s.cancel)
            dialog.add_response("disconnect", s.disconnect_all_and_quit)
            dialog.add_response("quit", s.quit)
            dialog.set_response_appearance("quit", Adw.ResponseAppearance.SUGGESTED)
            dialog.set_default_response("quit")
            dialog.set_close_response("cancel")
            answers = {"quit": QuitAnswer.QUIT, "disconnect": QuitAnswer.DISCONNECT_AND_QUIT, "cancel": QuitAnswer.CANCEL}
            dialog.connect("response", lambda _, response: on_answer(answers[response]))
            dialog.present(self._parent())

        self._scheduler.post(show)

    def confirm_discarding_edits(self, on_answer: Callable[[bool], None]) -> None:
        s = self._strings

        def show() -> None:
            dialog = Adw.AlertDialog(heading=s.quit_with_unsaved_changes, body=s.unsaved_changes_message)
            dialog.add_response("cancel", s.cancel)
            dialog.add_response("quit", s.quit)
            dialog.set_response_appearance("quit", Adw.ResponseAppearance.DESTRUCTIVE)
            dialog.set_default_response("cancel")
            dialog.set_close_response("cancel")
            dialog.connect("response", lambda _, response: on_answer(response == "quit"))
            dialog.present(self._parent())

        self._scheduler.post(show)
