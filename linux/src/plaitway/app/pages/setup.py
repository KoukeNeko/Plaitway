"""The helper is not ready: one thing to do about it. And the window with no profiles."""

from __future__ import annotations

import gi

gi.require_version("Gtk", "4.0")
gi.require_version("Adw", "1")
from gi.repository import Adw, Gtk  # noqa: E402

from ...core.app_model import AppModel  # noqa: E402
from ...core.setup import SetupContent  # noqa: E402
from ..widgets import follow, spinner, status_page, text_button  # noqa: E402


class SetupPage(Gtk.Box):
    """What replaces the window while the helper is not ready."""

    def __init__(self, model: AppModel) -> None:
        super().__init__()
        self._model = model
        self._strings = model.strings
        self._shown: SetupContent | None = None
        self._status = Adw.StatusPage(hexpand=True, vexpand=True)
        self.append(self._status)
        follow(self, [model.changed], self.update)

    def update(self) -> None:
        content = self._model.setup_content
        if content is None or content == self._shown:
            return
        self._shown = content
        self._status.set_icon_name(None if content.shows_progress else content.glyph)
        self._status.set_title(content.title)
        # The detail may be a path or a command the person wants to copy.
        self._status.set_description(content.detail or "")
        box = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=12, halign=Gtk.Align.CENTER)
        if content.shows_progress:
            box.append(spinner(width_request=32, height_request=32))
        if content.primary is not None:
            primary = content.primary
            button = text_button(primary.label(self._strings), lambda: self._model.perform(primary), css=["suggested-action", "pill"])
            box.append(button)
        if content.secondary is not None:
            secondary = content.secondary
            box.append(text_button(secondary.label(self._strings), lambda: self._model.perform(secondary), css=["flat"]))
        self._status.set_child(box)


def empty_profiles_page(model: AppModel) -> Adw.StatusPage:
    """No profiles yet: the one thing to do is to import one."""
    s = model.strings
    page = status_page("plaitway-state-idle-symbolic", s.no_profiles)
    page.set_child(text_button(s.import_profile, model.choose_import, css=["suggested-action", "pill"]))
    page.get_child().set_halign(Gtk.Align.CENTER)
    return page
