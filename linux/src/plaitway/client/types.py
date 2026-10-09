"""The types the app works with, under the names it uses.

Messages are the protobuf classes themselves, as in the macOS app: read-only by
convention once a store holds them. Enums drop their prefix, so
PROFILE_STATE_AWAITING_CREDENTIALS is ProfileState.AWAITING_CREDENTIALS; a
message field holds the number, which compares equal to the member.
"""

from __future__ import annotations

from dataclasses import dataclass

from .proto import enum_type, message

Profile = message("Profile")
ProfileSettings = message("ProfileSettings")
OnDemandRules = message("OnDemandRules")
ProfileSummary = message("ProfileSummary")
Endpoint = message("Endpoint")
TunnelStatus = message("TunnelStatus")
RouteStatus = message("RouteStatus")
DnsStatus = message("DnsStatus")
CredentialRequest = message("CredentialRequest")
DaemonInfo = message("DaemonInfo")
EngineInfo = message("EngineInfo")
Diagnostics = message("Diagnostics")
NetworkInfo = message("NetworkInfo")
OwnedRoute = message("OwnedRoute")
StaleRoute = message("StaleRoute")
JournalEntry = message("JournalEntry")
LogLine = message("LogLine")
ImportWarning = message("ImportWarning")
ProfileEvent = message("ProfileEvent")
ProfileSnapshot = message("ProfileSnapshot")

ProfileKind = enum_type("ProfileKind")
ProfileState = enum_type("ProfileState")
TunnelMode = enum_type("TunnelMode")
CredentialKind = enum_type("CredentialKind")
RouteState = enum_type("RouteState")
RouteKind = enum_type("RouteKind")
LogLevel = enum_type("LogLevel")


@dataclass(frozen=True)
class Credentials:
    """What a profile asked for. `username` is empty for a key passphrase."""

    password: str
    username: str = ""


@dataclass(frozen=True)
class ProfileImport:
    """An imported or edited profile and what the daemon removed or ignored in it."""

    profile: Profile
    warnings: list[ImportWarning]


def on_demand_is_active(rules: OnDemandRules) -> bool:
    """On-demand activation is on when either network type is checked."""
    return rules.ethernet or rules.wifi
