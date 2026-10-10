"""Where the helper stands, from what the app knows: the connection to the
daemon, what systemd says about the unit, and the versions."""

from __future__ import annotations

import enum
from dataclasses import dataclass

from ..client.errors import FailureKind
from ..l10n.strings import Strings
from .ports import HelperRegistration
from .profile_store import Connection


class SetupState(enum.Enum):
    READY = "ready"
    # Connected, but the helper is from another version of the app.
    VERSION_MISMATCH = "version mismatch"
    CONNECTING = "connecting"
    # plaitwayd.service is not there.
    NOT_INSTALLED = "not installed"
    # Installed and stopped: the app can start it.
    NOT_RUNNING = "not running"
    # Running and not answering, or systemd cannot be asked.
    NOT_RESPONDING = "not responding"
    # The system does not run systemd, which the app would ask to start the helper: it is not answering
    # and the person starts it with the service manager of the system.
    NOT_MANAGED = "not managed"
    # Running, and it refuses this user.
    PERMISSION_DENIED = "permission denied"
    # Answering, but from a socket that is not root's: the app sent nothing to it.
    SERVER_REFUSED = "server refused"
    # A daemon of the developer's own (PLAITWAY_DEV): the app does not manage the helper.
    DEVELOPMENT = "development"


@dataclass(frozen=True)
class DaemonSetup:
    state: SetupState
    daemon_version: str = ""
    app_version: str = ""
    # What refused the app, when the state says so.
    detail: str = ""

    @property
    def is_usable(self) -> bool:
        """The daemon answers and can be given commands."""
        return self.state in (SetupState.READY, SetupState.VERSION_MISMATCH)


def resolve_setup(
    *,
    connection: Connection,
    registration: HelperRegistration | None,
    failure_kind: FailureKind | None,
    failure_message: str = "",
    daemon_version: str | None,
    app_version: str | None,
    is_overridden: bool,
    is_settling: bool,
) -> DaemonSetup:
    """
    registration: None until systemd was asked.
    is_settling: the helper was just started or retried and may still be starting.
    """
    if connection is Connection.CONNECTED:
        if not is_overridden and app_version and daemon_version and app_version != daemon_version:
            return DaemonSetup(SetupState.VERSION_MISMATCH, daemon_version, app_version)
        return DaemonSetup(SetupState.READY)

    connecting = connection is Connection.CONNECTING
    if is_overridden:
        return DaemonSetup(SetupState.CONNECTING if connecting else SetupState.DEVELOPMENT)
    if not connecting and failure_kind is FailureKind.SERVER_REFUSED:
        return DaemonSetup(SetupState.SERVER_REFUSED, detail=failure_message)
    if not connecting and failure_kind is FailureKind.PERMISSION_DENIED:
        return DaemonSetup(SetupState.PERMISSION_DENIED)
    if registration is None:
        return DaemonSetup(SetupState.CONNECTING)
    # While connecting, a helper that something else started may be about to answer.
    match registration:
        case HelperRegistration.NOT_INSTALLED:
            return DaemonSetup(SetupState.CONNECTING if connecting else SetupState.NOT_INSTALLED)
        case HelperRegistration.NO_SYSTEMD:
            return DaemonSetup(SetupState.CONNECTING if connecting else SetupState.NOT_MANAGED)
        case HelperRegistration.STOPPED:
            return DaemonSetup(SetupState.CONNECTING if connecting or is_settling else SetupState.NOT_RUNNING)
        case HelperRegistration.RUNNING:
            return DaemonSetup(SetupState.CONNECTING if connecting or is_settling else SetupState.NOT_RESPONDING)
        case _:
            return DaemonSetup(SetupState.CONNECTING if connecting else SetupState.NOT_RESPONDING)


class SetupAction(enum.Enum):
    START_HELPER = "start helper"
    RESTART_HELPER = "restart helper"
    RETRY = "retry"

    def label(self, strings: Strings) -> str:
        match self:
            case SetupAction.START_HELPER:
                return strings.start_helper
            case SetupAction.RESTART_HELPER:
                return strings.restart_helper
            case SetupAction.RETRY:
                return strings.retry


@dataclass(frozen=True)
class SetupContent:
    """What the window shows instead of the profiles while the helper is not ready."""

    glyph: str
    title: str
    detail: str | None
    shows_progress: bool
    primary: SetupAction | None
    secondary: SetupAction | None


# Where the helper writes its log when it cannot be asked for it.
HELPER_LOG_LOCATION = "journalctl -u plaitwayd.service"


def menu_status(setup: DaemonSetup, strings: Strings) -> str | None:
    """The state as the tray menu's disabled first line says it; None when there is nothing to say."""
    match setup.state:
        case SetupState.READY:
            return None
        case SetupState.VERSION_MISMATCH:
            return strings.helper_out_of_date
        case SetupState.CONNECTING:
            return strings.connecting
        case SetupState.NOT_INSTALLED:
            return strings.helper_not_installed
        case SetupState.NOT_RUNNING | SetupState.NOT_MANAGED:
            return strings.helper_not_running
        case SetupState.PERMISSION_DENIED:
            return strings.permission_denied
        case SetupState.SERVER_REFUSED:
            return strings.helper_not_trusted
        case _:
            return strings.helper_unavailable


def setup_content(setup: DaemonSetup, strings: Strings) -> SetupContent | None:
    """The window's content; None while the profiles can be shown."""
    s = strings
    match setup.state:
        case SetupState.READY | SetupState.VERSION_MISMATCH:
            return None
        case SetupState.CONNECTING:
            return SetupContent("plaitway-state-idle-symbolic", s.connecting, None, True, None, None)
        case SetupState.NOT_INSTALLED:
            return SetupContent(
                "plaitway-state-idle-symbolic", s.helper_not_installed, s.install_the_package, False,
                SetupAction.RETRY, None,
            )
        case SetupState.NOT_RUNNING:
            return SetupContent(
                "plaitway-state-idle-symbolic", s.helper_not_running, None, False, SetupAction.START_HELPER, None
            )
        case SetupState.NOT_MANAGED:
            return SetupContent(
                "plaitway-state-idle-symbolic", s.helper_not_running, s.start_helper_without_systemd,
                False, SetupAction.RETRY, None,
            )
        case SetupState.NOT_RESPONDING:
            # The unit is fine; the log is the only trace of why the daemon does not answer.
            detail = "\n".join([s.running_not_answering, s.helper_log_location(HELPER_LOG_LOCATION)])
            return SetupContent(
                "dialog-warning-symbolic", s.helper_unavailable, detail, False,
                SetupAction.RETRY, SetupAction.RESTART_HELPER,
            )
        case SetupState.PERMISSION_DENIED:
            return SetupContent(
                "changes-prevent-symbolic", s.permission_denied, s.console_user_only, False, SetupAction.RETRY, None
            )
        case SetupState.SERVER_REFUSED:
            return SetupContent(
                "dialog-warning-symbolic", s.helper_not_trusted, setup.detail or None, False, SetupAction.RETRY, None
            )
        case SetupState.DEVELOPMENT:
            return SetupContent(
                "applications-engineering-symbolic", s.helper_unavailable, s.start_development_daemon, False,
                SetupAction.RETRY, None,
            )
