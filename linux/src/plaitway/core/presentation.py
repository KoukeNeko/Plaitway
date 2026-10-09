"""How the states and rows of the daemon are named and drawn.

Every state has a shape of its own, a word and a colour, in that order of
importance: the colour alone fails people who cannot tell green from red.
`glyph` names a symbolic icon, `tone` the colour the view gives it.
"""

from __future__ import annotations

import enum
from dataclasses import dataclass

from ..client.types import (
    ImportWarning,
    LogLevel,
    Profile,
    ProfileKind,
    ProfileState,
    RouteKind,
    RouteState,
    TunnelMode,
)
from ..l10n.strings import Strings


class Tone(enum.Enum):
    NORMAL = "normal"
    SUCCESS = "success"
    WARNING = "warning"
    ERROR = "error"
    MUTED = "muted"


PROFILE_GLYPHS = {
    ProfileState.CONNECTED: "plaitway-state-connected-symbolic",
    ProfileState.CONNECTING: "plaitway-state-connecting-symbolic",
    ProfileState.RECONNECTING: "plaitway-state-reconnecting-symbolic",
    ProfileState.DISCONNECTING: "plaitway-state-disconnecting-symbolic",
    ProfileState.AWAITING_CREDENTIALS: "plaitway-state-credentials-symbolic",
    ProfileState.FAILED: "plaitway-state-attention-symbolic",
    ProfileState.DISCONNECTED: "plaitway-state-idle-symbolic",
    ProfileState.UNSPECIFIED: "plaitway-state-idle-symbolic",
}

PROFILE_TONES = {
    ProfileState.CONNECTED: Tone.SUCCESS,
    ProfileState.CONNECTING: Tone.WARNING,
    ProfileState.RECONNECTING: Tone.WARNING,
    ProfileState.AWAITING_CREDENTIALS: Tone.WARNING,
    ProfileState.DISCONNECTING: Tone.WARNING,
    ProfileState.FAILED: Tone.ERROR,
    ProfileState.DISCONNECTED: Tone.MUTED,
    ProfileState.UNSPECIFIED: Tone.MUTED,
}

TRANSITIONAL_STATES = frozenset({ProfileState.CONNECTING, ProfileState.RECONNECTING, ProfileState.DISCONNECTING})

ROUTE_GLYPHS = {
    RouteState.INSTALLED: "object-select-symbolic",
    RouteState.PENDING: "content-loading-symbolic",
    RouteState.SHADOWED: "list-remove-symbolic",
    RouteState.BLOCKED: "dialog-warning-symbolic",
    RouteState.FAILED: "dialog-error-symbolic",
    RouteState.UNSPECIFIED: "dialog-question-symbolic",
}

# A shadowed prefix is a profile standing by behind a higher priority one, which
# is how it is meant to work: neutral. A blocked one is a conflict the person may
# want to know about.
ROUTE_TONES = {
    RouteState.INSTALLED: Tone.SUCCESS,
    RouteState.PENDING: Tone.WARNING,
    RouteState.BLOCKED: Tone.WARNING,
    RouteState.FAILED: Tone.ERROR,
    RouteState.SHADOWED: Tone.MUTED,
    RouteState.UNSPECIFIED: Tone.MUTED,
}

TUNNEL_MODE_CHOICES = (TunnelMode.AUTO, TunnelMode.FULL, TunnelMode.SPLIT)


def profile_glyph(state: int) -> str:
    return PROFILE_GLYPHS[ProfileState(state)]


def profile_tone(state: int) -> Tone:
    return PROFILE_TONES[ProfileState(state)]


def is_transitional(state: int) -> bool:
    return ProfileState(state) in TRANSITIONAL_STATES


def route_glyph(state: int) -> str:
    return ROUTE_GLYPHS[RouteState(state)]


def route_tone(state: int) -> Tone:
    return ROUTE_TONES[RouteState(state)]


def is_lost(state: int) -> bool:
    """The prefix is not in the routing table although the profile asks for it."""
    return RouteState(state) in (RouteState.BLOCKED, RouteState.FAILED)


def log_tag(level: int) -> str:
    """Log level names are the same in every language."""
    match LogLevel(level):
        case LogLevel.DEBUG:
            return "DEBUG"
        case LogLevel.WARN:
            return "WARN"
        case LogLevel.ERROR:
            return "ERROR"
        case _:
            return "INFO"


def log_tone(level: int) -> Tone:
    match LogLevel(level):
        case LogLevel.WARN:
            return Tone.WARNING
        case LogLevel.ERROR:
            return Tone.ERROR
        case LogLevel.DEBUG:
            return Tone.MUTED
        case _:
            return Tone.NORMAL


class Presenter:
    """The words for the states and rows, in one language."""

    def __init__(self, strings: Strings) -> None:
        self.strings = strings

    def state_label(self, state: int) -> str:
        s = self.strings
        match ProfileState(state):
            case ProfileState.CONNECTED:
                return s.connected
            case ProfileState.CONNECTING:
                return s.connecting
            case ProfileState.DISCONNECTING:
                return s.disconnecting
            case ProfileState.DISCONNECTED:
                return s.disconnected
            case ProfileState.FAILED:
                return s.failed
            case ProfileState.RECONNECTING:
                return s.reconnecting
            case ProfileState.AWAITING_CREDENTIALS:
                return s.awaiting_credentials
            case _:
                return s.unknown

    @staticmethod
    def kind_label(kind: int) -> str | None:
        """Protocol names are product names and stay untranslated; None for a kind the app does not know."""
        match ProfileKind(kind):
            case ProfileKind.OPENVPN:
                return "OpenVPN"
            case ProfileKind.WIREGUARD:
                return "WireGuard"
            case _:
                return None

    def kind_text(self, kind: int) -> str:
        return self.kind_label(kind) or self.strings.unknown

    def tunnel_mode_label(self, mode: int) -> str:
        match TunnelMode(mode):
            case TunnelMode.FULL:
                return self.strings.full_tunnel
            case TunnelMode.SPLIT:
                return self.strings.split_tunnel
            case _:
                return self.strings.auto

    def route_state_label(self, state: int, shadowed_by: str | None = None) -> str:
        s = self.strings
        match RouteState(state):
            case RouteState.INSTALLED:
                return s.installed
            case RouteState.PENDING:
                return s.pending
            case RouteState.SHADOWED:
                return s.shadowed_by(shadowed_by) if shadowed_by else s.shadowed
            case RouteState.BLOCKED:
                return s.blocked_by_local_network
            case RouteState.FAILED:
                return s.failed
            case _:
                return s.unknown

    def route_kind_label(self, kind: int) -> str:
        s = self.strings
        match RouteKind(kind):
            case RouteKind.TUNNEL:
                return s.tunnel
            case RouteKind.BYPASS:
                return s.bypass
            case RouteKind.DEFAULT:
                return s.default_route
            case _:
                return s.unknown

    def profile_subtitle(self, profile: Profile) -> str:
        """`OpenVPN · Connected`, under a profile's name."""
        return f"{self.kind_text(profile.kind)} · {self.state_label(profile.state)}"

    def warning_summary(self, warning: ImportWarning) -> str:
        """`Line 4: up – removed, a profile cannot run programs`."""
        place = self.strings.line_number(warning.line) + ": " if warning.line > 0 else ""
        return place + " – ".join(part for part in (warning.directive, warning.message) if part)


@dataclass(frozen=True)
class RouteRow:
    """A row of the Routes list: what the daemon installed for a profile, or,
    while it is not connected, the prefixes the profile names itself."""

    id: int  # the position in the list: the same prefix can be listed more than once
    prefix: str
    state: int | None
    state_label: str | None
    detail: str  # the daemon's own explanation; empty when there is none

    @staticmethod
    def rows(profile: Profile, name_of, presenter: Presenter) -> list[RouteRow]:
        """`name_of` maps a profile id to the name shown for it, or None."""
        if profile.status.routes:
            return [
                RouteRow(
                    index,
                    route.prefix,
                    route.state,
                    presenter.route_state_label(
                        route.state,
                        (name_of(route.shadowed_by) or route.shadowed_by) if route.shadowed_by else None,
                    ),
                    route.detail,
                )
                for index, route in enumerate(profile.status.routes)
            ]
        return [RouteRow(index, prefix, None, None, "") for index, prefix in enumerate(profile.summary.routes)]


@dataclass(frozen=True)
class DnsRow:
    """A DNS entry the daemon installed for a profile."""

    id: int
    servers: str
    domains: str
    state: int
    detail: str

    @staticmethod
    def rows(profile: Profile, strings: Strings) -> list[DnsRow]:
        return [
            DnsRow(
                index,
                ", ".join(dns.servers),
                # "." is the daemon's way of saying every domain.
                ", ".join(strings.all_domains if domain == "." else domain for domain in dns.match_domains),
                dns.state,
                dns.detail,
            )
            for index, dns in enumerate(profile.status.dns)
        ]
