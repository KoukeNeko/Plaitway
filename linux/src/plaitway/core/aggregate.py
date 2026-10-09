"""What the tray icon says about all profiles together."""

from __future__ import annotations

import enum

from ..client.types import Profile, ProfileState
from ..l10n.strings import Strings
from .setup import DaemonSetup, SetupState


class AggregateState(enum.Enum):
    UNAVAILABLE = "unavailable"
    IDLE = "idle"
    CONNECTED = "connected"
    CONNECTING = "connecting"
    NEEDS_CREDENTIALS = "needs credentials"
    PROBLEM = "problem"

    @classmethod
    def of(cls, setup: DaemonSetup, profiles: list[Profile]) -> AggregateState:
        """What needs the person comes before what is under way: a profile
        waiting for a password does not get there by itself."""
        if setup.state is SetupState.CONNECTING:
            return cls.CONNECTING
        if not setup.is_usable:
            return cls.UNAVAILABLE
        states = {profile.state for profile in profiles}
        if ProfileState.FAILED in states:
            return cls.PROBLEM
        if ProfileState.AWAITING_CREDENTIALS in states:
            return cls.NEEDS_CREDENTIALS
        if states & {ProfileState.CONNECTING, ProfileState.RECONNECTING, ProfileState.DISCONNECTING}:
            return cls.CONNECTING
        if ProfileState.CONNECTED in states:
            return cls.CONNECTED
        return cls.IDLE

    @property
    def tray_icon(self) -> str:
        """One of four: what needs the person looks the same."""
        match self:
            case AggregateState.IDLE:
                return "plaitway-state-idle-symbolic"
            case AggregateState.CONNECTING:
                return "plaitway-state-connecting-symbolic"
            case AggregateState.CONNECTED:
                return "plaitway-state-connected-symbolic"
            case _:
                return "plaitway-state-attention-symbolic"

    def label(self, strings: Strings) -> str:
        match self:
            case AggregateState.UNAVAILABLE:
                return strings.helper_unavailable
            case AggregateState.IDLE:
                return strings.not_connected
            case AggregateState.CONNECTED:
                return strings.connected
            case AggregateState.CONNECTING:
                return strings.connecting
            case AggregateState.NEEDS_CREDENTIALS:
                return strings.awaiting_credentials
            case AggregateState.PROBLEM:
                return strings.problem
