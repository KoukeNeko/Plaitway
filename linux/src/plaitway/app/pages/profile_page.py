"""A profile's five pages. Which one is open is the app model's: it stays when another
profile is selected."""

from __future__ import annotations

import gi

gi.require_version("Gtk", "4.0")
gi.require_version("Adw", "1")
from gi.repository import Adw, Gtk  # noqa: E402

from ...core.app_model import AppModel, ProfileSection  # noqa: E402
from .configuration import ConfigurationPage
from .icons import ICONS
from .logs import LogsPage
from .overview import OverviewPage
from .profile_settings import ProfileSettingsPage
from .routes import RoutesPage

def build_profile_pages(model: AppModel, profile_id: str) -> Adw.ViewStack:
    """The pages of one profile. A profile of its own: typed text and the log stream do
    not carry over from another."""
    stack = Adw.ViewStack(hhomogeneous=False)
    strings = model.strings
    pages: dict[ProfileSection, Gtk.Widget] = {
        ProfileSection.OVERVIEW: OverviewPage(model, profile_id, lambda: model.show_section(ProfileSection.ROUTES)),
        ProfileSection.ROUTES: RoutesPage(model, profile_id),
        ProfileSection.LOGS: LogsPage(model, profile_id),
        ProfileSection.CONFIGURATION: ConfigurationPage(model, profile_id),
        ProfileSection.SETTINGS: ProfileSettingsPage(model, profile_id),
    }
    for section, page in pages.items():
        stack.add_titled_with_icon(page, section.name.lower(), section.label(strings), ICONS[section.name.lower()])
    return stack
