"""What the tray item's menu lists: a row per profile, and no buttons, so the
row does what the profile needs."""

from __future__ import annotations

import enum
from dataclasses import dataclass

from ..client.types import Profile, ProfileState
from ..l10n.strings import Strings
from .aggregate import AggregateState
from .presentation import Presenter
from .setup import DaemonSetup, menu_status


class TrayAction(enum.Enum):
    CONNECT = "connect"
    DISCONNECT = "disconnect"
    # A failed profile is switched on already; asking again retries.
    RETRY = "retry"
    # The credential dialog is in the window.
    ANSWER_CREDENTIALS = "answer credentials"


class Mark(enum.Enum):
    OFF = "off"
    ON = "on"
    MIXED = "mixed"


@dataclass(frozen=True)
class TrayItem:
    profile_id: str
    title: str
    subtitle: str
    mark: Mark
    action: TrayAction
    is_enabled: bool

    @staticmethod
    def of(profile: Profile, presenter: Presenter) -> TrayItem:
        match ProfileState(profile.state):
            case ProfileState.CONNECTED:
                mark, action = Mark.ON, TrayAction.DISCONNECT
            case ProfileState.CONNECTING | ProfileState.RECONNECTING | ProfileState.DISCONNECTING:
                mark, action = Mark.MIXED, TrayAction.DISCONNECT
            case ProfileState.FAILED:
                mark, action = Mark.OFF, TrayAction.RETRY
            case ProfileState.AWAITING_CREDENTIALS:
                mark, action = Mark.MIXED, TrayAction.ANSWER_CREDENTIALS
            case _:
                mark, action = Mark.OFF, TrayAction.CONNECT
        return TrayItem(
            profile.id,
            profile.name,
            presenter.profile_subtitle(profile),
            mark,
            action,
            profile.state != ProfileState.DISCONNECTING,
        )


@dataclass(frozen=True)
class TrayMenu:
    status: str | None  # why the profiles below cannot be used, when they cannot
    summary: str | None  # the first line above the profiles: how the connections stand together
    items: tuple[TrayItem, ...]
    can_disconnect_all: bool
    can_import: bool

    @staticmethod
    def of(setup: DaemonSetup, profiles: list[Profile], strings: Strings) -> TrayMenu:
        presenter = Presenter(strings)
        # Profiles of a daemon that is not answering would show states that are no longer true.
        shown = profiles if setup.is_usable else []
        return TrayMenu(
            status=menu_status(setup, strings),
            summary=_summary(setup, shown, strings) if shown else None,
            items=tuple(TrayItem.of(profile, presenter) for profile in shown),
            can_disconnect_all=any(profile.desired_enabled for profile in shown),
            can_import=setup.is_usable,
        )


def _summary(setup: DaemonSetup, profiles: list[Profile], strings: Strings) -> str:
    aggregate = AggregateState.of(setup, profiles)
    connected = sum(1 for profile in profiles if profile.state == ProfileState.CONNECTED)
    if aggregate is AggregateState.CONNECTED or (connected > 0 and aggregate is AggregateState.CONNECTING):
        return strings.connected_count(connected)
    return aggregate.label(strings)
