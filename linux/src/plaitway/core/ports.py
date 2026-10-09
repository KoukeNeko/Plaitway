"""What the models need from the platform, as interfaces.

The GTK app implements them (plaitway.app); core/fakes.py holds the doubles the
tests use. A port that shows something to the person is called from any thread
and does its work on the UI thread; its answers arrive there too.
"""

from __future__ import annotations

import enum
from collections.abc import Callable
from dataclasses import dataclass
from typing import Protocol

from ..client.types import Credentials, CredentialKind
from .import_report import ImportReport

Cancel = Callable[[], None]


class UiScheduler(Protocol):
    """The UI thread's main loop."""

    def post(self, fn: Callable[[], None]) -> None:
        """Runs `fn` on the UI thread soon. Callable from any thread."""

    def call_later(self, seconds: float, fn: Callable[[], None]) -> Cancel:
        """Runs `fn` on the UI thread after `seconds`. The result cancels it.
        Callable from any thread."""


class Clipboard(Protocol):
    def set_text(self, text: str) -> None: ...


class FilePicker(Protocol):
    def choose_profiles(self, on_chosen: Callable[[list[str]], None]) -> None:
        """Asks for profile files (any file: the daemon tells OpenVPN from WireGuard
        by content). `on_chosen` is not called when the person cancels."""


class CredentialStoreError(Exception):
    """The credential store could not do what was asked."""

    def __init__(self, message: str, *, unavailable: bool = False) -> None:
        super().__init__(message)
        # There is no Secret Service to ask, as opposed to one that said no.
        self.unavailable = unavailable


class CredentialStore(Protocol):
    """Where the user's answers to credential requests are kept between
    connections. The daemon never keeps them on disk, so this is the only copy.
    The calls block (a keyring may stop to ask for its password) and raise
    CredentialStoreError; call them off the UI thread."""

    def lookup(self, profile_id: str, kind: CredentialKind) -> Credentials | None: ...

    def has(self, profile_id: str, kind: CredentialKind) -> bool:
        """Whether there is an answer, without reading it; that needs no permission."""

    def save(self, credentials: Credentials, profile_id: str, kind: CredentialKind) -> None: ...

    def remove(self, profile_id: str, kind: CredentialKind) -> None: ...


class HelperRegistration(enum.Enum):
    """The helper (plaitwayd.service) as systemd reports it."""

    NOT_INSTALLED = "not installed"  # LoadState not-found
    STOPPED = "stopped"  # installed, not running
    RUNNING = "running"
    UNKNOWN = "unknown"  # systemd cannot be asked


class HelperServiceError(Exception):
    """Starting or restarting the helper did not work, or was not allowed."""


class HelperService(Protocol):
    """The systemd unit of the helper. Calls block; call them off the UI thread."""

    def registration(self) -> HelperRegistration: ...

    def start(self) -> None:
        """Starts the unit; polkit asks the person when it has to."""

    def restart(self) -> None: ...


class LoginItem(Protocol):
    """Starting the app when the person logs in."""

    @property
    def is_enabled(self) -> bool: ...

    def set_enabled(self, enabled: bool) -> None: ...


class QuitAnswer(enum.Enum):
    QUIT = "quit"
    DISCONNECT_AND_QUIT = "disconnect and quit"
    CANCEL = "cancel"


class QuitPrompts(Protocol):
    def ask_quit(self, on_answer: Callable[[QuitAnswer], None]) -> None:
        """What Quit asks while profiles are switched on: the helper, not the app, keeps them connected."""

    def confirm_discarding_edits(self, on_answer: Callable[[bool], None]) -> None:
        """Edits of profile text that were not saved go with the app. True when the person quits anyway."""


@dataclass(frozen=True)
class Confirmation:
    """A question that a button answers: `action` does it, Cancel does not."""

    title: str
    message: str
    action: str
    destructive: bool = True


@dataclass(frozen=True)
class CredentialPrompt:
    """A profile that waits for the user to type what the daemon asked for."""

    profile_id: str
    kind: CredentialKind
    # The daemon refused what was sent before; `message` is its reason, which may be empty.
    rejected: bool
    message: str


class Dialogs(Protocol):
    def show_alert(self, title: str, message: str) -> None:
        """Something a command did not manage."""

    def confirm(self, confirmation: Confirmation, on_answer: Callable[[bool], None]) -> None: ...

    def show_import_report(self, report: ImportReport) -> None:
        """What came of importing the files the person picked or dropped."""

    def ask_credentials(
        self,
        prompt: CredentialPrompt,
        profile_name: str,
        on_submit: Callable[[str, str], None],
        on_cancel: Callable[[], None],
    ) -> None: ...

    def dismiss_credentials(self, profile_id: str) -> None:
        """The profile does not ask any more."""


class Localizer(Protocol):
    language: str


@dataclass(frozen=True)
class Platform:
    """The ports together, as the app gives them to its models."""

    scheduler: UiScheduler
    clipboard: Clipboard
    file_picker: FilePicker
    helper: HelperService
    login_item: LoginItem
    dialogs: Dialogs
    quit_prompts: QuitPrompts
