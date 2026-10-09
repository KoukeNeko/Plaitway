"""The ports that are one call of GLib, Gdk or Gtk each."""

from __future__ import annotations

import logging
from collections.abc import Callable

import gi

gi.require_version("Gtk", "4.0")
from gi.repository import Gdk, Gio, GLib, Gtk  # noqa: E402

from ..core.ports import Cancel  # noqa: E402
from ..l10n.strings import Strings  # noqa: E402

log = logging.getLogger(__name__)


class GLibScheduler:
    """The UI thread is the thread of the GLib main loop."""

    def post(self, fn: Callable[[], None]) -> None:
        def run() -> bool:
            self._call(fn)
            return GLib.SOURCE_REMOVE

        GLib.idle_add(run)

    def call_later(self, seconds: float, fn: Callable[[], None]) -> Cancel:
        pending = True

        def run() -> bool:
            nonlocal pending
            pending = False
            self._call(fn)
            return GLib.SOURCE_REMOVE

        source = GLib.timeout_add(max(1, int(seconds * 1000)), run)

        def cancel() -> None:
            nonlocal pending
            if pending:
                pending = False
                GLib.source_remove(source)

        return cancel

    @staticmethod
    def _call(fn: Callable[[], None]) -> None:
        try:
            fn()
        except Exception:
            log.exception("a call on the UI thread failed")


class GdkClipboard:
    def set_text(self, text: str) -> None:
        display = Gdk.Display.get_default()
        if display is not None:
            display.get_clipboard().set(text)


class GtkFilePicker:
    """The file chooser for importing profiles. Any file can be chosen: the daemon
    tells an OpenVPN profile from a WireGuard one by its content."""

    def __init__(self, strings: Strings, parent: Callable[[], Gtk.Window | None]) -> None:
        self._strings = strings
        self._parent = parent

    def choose_profiles(self, on_chosen: Callable[[list[str]], None]) -> None:
        dialog = Gtk.FileDialog()
        dialog.set_title(self._strings.import_profile)
        dialog.set_accept_label(self._strings.import_label)

        def finished(source: Gtk.FileDialog, result: Gio.AsyncResult) -> None:
            try:
                files = source.open_multiple_finish(result)
            except GLib.Error as error:
                # Cancelling is not a failure.
                if not error.matches(Gtk.dialog_error_quark(), Gtk.DialogError.DISMISSED):
                    log.error("the file chooser failed: %s", error.message)
                return
            paths = [file.get_path() for file in files if file.get_path()]
            if paths:
                on_chosen(paths)

        dialog.open_multiple(self._parent(), None, finished)
