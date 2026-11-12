"""The About page: the icon, the name and the version, where the source is, the licence, and the people
who contributed."""

from __future__ import annotations

import gi

gi.require_version("Gtk", "4.0")
gi.require_version("Adw", "1")
from gi.repository import Adw, GLib, Gtk  # noqa: E402

from ...core import contributors  # noqa: E402
from ...core.app_model import AppModel  # noqa: E402
from ...core.contributors import Contributor  # noqa: E402
from ...version import __version__  # noqa: E402
from .. import paths  # noqa: E402
from ..widgets import label  # noqa: E402


class AboutPage(Adw.PreferencesPage):
    def __init__(self, model: AppModel) -> None:
        super().__init__()
        self._model = model
        s = model.strings

        identity = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=4, margin_top=12, margin_bottom=12)
        identity.append(Gtk.Image(icon_name=paths.APP_ID, pixel_size=96, accessible_role=Gtk.AccessibleRole.PRESENTATION))
        identity.append(label(paths.APP_NAME, css=["title-1"], xalign=0.5))
        identity.append(label(model.app_version or __version__, css=["dimmed"], xalign=0.5))
        header = Adw.PreferencesGroup()
        header.add(identity)
        self.add(header)

        links = Adw.PreferencesGroup()
        links.add(self._link_row(s.repository, contributors.REPOSITORY_URL))
        links.add(Adw.ActionRow(title=s.license, subtitle=paths.LICENSE_NAME))
        self.add(links)

        self._contributors = Adw.PreferencesGroup(title=s.contributors)
        self._contributor_rows: list[Adw.ActionRow] = []
        self._contributors.set_visible(False)
        self.add(self._contributors)

        self.connect("map", self._mapped)

    def _link_row(self, title: str, url: str, subtitle: str | None = None) -> Adw.ActionRow:
        row = Adw.ActionRow(title=title, subtitle=GLib.markup_escape_text(subtitle or url), activatable=True)
        row.add_suffix(Gtk.Image(icon_name="adw-external-link-symbolic", accessible_role=Gtk.AccessibleRole.PRESENTATION))
        row.connect("activated", lambda _: Gtk.UriLauncher.new(url).launch(self.get_root(), None, None))
        return row

    def _mapped(self, *_) -> None:
        # The package carries the list its build made. A checkout has none and asks GitHub, as a developer
        # wants the people who contributed so far; the answer is not kept.
        people = contributors.baked()
        if people or not paths.from_checkout():
            self._show_contributors(people)
            return
        self._model.run_job(contributors.fetch, self._fetched)

    def _fetched(self, outcome) -> None:
        if outcome.ok:
            self._show_contributors(outcome.value)

    def _show_contributors(self, people: list[Contributor]) -> None:
        for row in self._contributor_rows:
            self._contributors.remove(row)
        self._contributor_rows.clear()
        for person in people:
            row = self._link_row(person.login, person.url, str(person.contributions))
            self._contributors.add(row)
            self._contributor_rows.append(row)
        self._contributors.set_visible(bool(people))
