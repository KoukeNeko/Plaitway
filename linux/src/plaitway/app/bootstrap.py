"""Builds the models and the ports the app runs on."""

from __future__ import annotations

import logging
from collections.abc import Callable
from dataclasses import dataclass

from gi.repository import Gtk

from ..client import location
from ..client.daemon_client import DaemonClient
from ..core.app_model import AppModel
from ..core.app_state import AppState
from ..core.credentials import InMemoryCredentialStore
from ..core.ports import CredentialStore, Platform
from ..core.profile_store import ProfileStore
from ..core.tasks import TaskRunner
from ..l10n.localizer import EnvironmentLocalizer
from ..l10n.strings import Strings
from ..version import __version__
from .dialogs import GtkDialogs, GtkQuitPrompts
from .login_item import AutostartLoginItem
from .platform import GdkClipboard, GLibScheduler, GtkFilePicker
from .secret_service import SecretServiceStore
from .systemd import SystemdHelperService

log = logging.getLogger(__name__)


@dataclass
class Services:
    model: AppModel
    client: DaemonClient
    runner: TaskRunner

    def shutdown(self) -> None:
        self.model.stop()
        self.client.close()
        self.runner.shutdown()


def build(parent: Callable[[], Gtk.Window | None]) -> Services:
    """`parent` is the window the dialogs belong to; it shows the window if it is hidden."""
    strings = Strings(EnvironmentLocalizer().language)
    override = location.override()
    is_development = override is not None

    client = DaemonClient(location.socket_path(), verify_owner=not is_development)
    # A daemon behind PLAITWAY_SOCKET is not the helper: the keyring is neither read to
    # answer it nor cleaned up on its word.
    credentials: CredentialStore = InMemoryCredentialStore() if is_development else SecretServiceStore()
    if is_development:
        log.warning("using the development daemon at %s; passwords are kept in memory only", override)

    scheduler = GLibScheduler()
    runner = TaskRunner(scheduler)
    store = ProfileStore(client, credentials, scheduler, runner)
    platform = Platform(
        scheduler=scheduler,
        clipboard=GdkClipboard(),
        file_picker=GtkFilePicker(strings, parent),
        helper=SystemdHelperService(),
        login_item=AutostartLoginItem(),
        dialogs=GtkDialogs(strings, scheduler, parent),
        quit_prompts=GtkQuitPrompts(strings, scheduler, parent),
    )
    model = AppModel(
        store, platform, runner, strings, AppState(), app_version=__version__, is_overridden=is_development
    )
    return Services(model, client, runner)
