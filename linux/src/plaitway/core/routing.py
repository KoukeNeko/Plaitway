"""The Routing page's data: every prefix a connected profile asks for, who holds it and who waits behind.

The daemon decides who holds a route; this only regroups what it says, by prefix instead of by
profile. The Windows app has the same page (`windows/rust/plaitway-core/src/model.rs`).
"""

from __future__ import annotations

import enum
from dataclasses import dataclass

from ..client.profile_set import ProfileSet
from ..client.types import Profile, ProfileState, RouteState
from ..l10n.strings import Strings
from .presentation import Presenter

DEFAULT_ROUTES = ("0.0.0.0/0", "::/0")
# The daemon installs a default route as two halves so that it outranks the one it replaces.
_DEFAULT_HALVES = (("0.0.0.0/1", "128.0.0.0/1", "0.0.0.0/0"), ("::/1", "8000::/1", "::/0"))
# One colour per profile, picked from the profile's rank by id so that it stays when the priorities change.
HUE_COUNT = 6


class Standing(enum.Enum):
    HOLDS = "holds"
    #: A profile with higher priority owns the prefix.
    WAITS = "waits"
    BLOCKED = "blocked"
    FAILED = "failed"
    PENDING = "pending"


_STANDINGS = {
    RouteState.INSTALLED: Standing.HOLDS,
    RouteState.SHADOWED: Standing.WAITS,
    RouteState.BLOCKED: Standing.BLOCKED,
    RouteState.FAILED: Standing.FAILED,
}


@dataclass(frozen=True)
class Request:
    prefix: str
    standing: Standing


@dataclass(frozen=True)
class BayRoute:
    """One prefix and every connected profile asking for it."""

    prefix: str
    #: Index into the profiles, highest priority first.
    holder: int | None
    #: (profile index, standing), in priority order.
    requests: tuple[tuple[int, Standing], ...]


def requests_of(profile: Profile) -> list[Request]:
    """What a connected profile asks for; nothing while it is not connected."""
    requests = [Request(route.prefix, _STANDINGS.get(RouteState(route.state), Standing.PENDING)) for route in profile.status.routes]
    for low, high, whole in _DEFAULT_HALVES:
        at = {request.prefix: index for index, request in reversed(list(enumerate(requests)))}
        if low in at and high in at and requests[at[low]].standing == requests[at[high]].standing:
            requests[at[low]] = Request(whole, requests[at[low]].standing)
            del requests[at[high]]
    return requests


def routes_of(profiles: ProfileSet) -> list[BayRoute]:
    """Default routes first, the rest in the order the profiles (by priority) name them."""
    rows: dict[str, tuple[int | None, list[tuple[int, Standing]]]] = {}
    for index, profile in enumerate(profiles):
        for request in requests_of(profile):
            _, asked = rows.setdefault(request.prefix, (None, []))
            asked.append((index, request.standing))
            if request.standing is Standing.HOLDS:
                rows[request.prefix] = (index, asked)
    ordered = sorted(rows.items(), key=lambda item: item[0] not in DEFAULT_ROUTES)  # stable
    return [BayRoute(prefix, holder, tuple(asked)) for prefix, (holder, asked) in ordered]


def holds(profile: Profile) -> tuple[int, int]:
    """(prefixes held, prefixes the profile asks for)."""
    requests = requests_of(profile)
    held = sum(1 for request in requests if request.standing is Standing.HOLDS)
    return held, max(len(profile.summary.routes), len(requests))


def hue_index(profiles: ProfileSet, profile_id: str) -> int:
    ids = sorted(profiles.ids)
    return ids.index(profile_id) % HUE_COUNT if profile_id in ids else 0


def route_title(prefix: str, strings: Strings) -> tuple[str, str]:
    """(name, description) of a prefix; the description is empty for one that is no default route."""
    if prefix == "0.0.0.0/0":
        return strings.default_route, strings.all_internet_traffic
    if prefix == "::/0":
        return f"{strings.default_route} (IPv6)", strings.all_internet_traffic
    return prefix, ""


def route_description(route: BayRoute, profiles: ProfileSet, strings: Strings) -> str:
    description = route_title(route.prefix, strings)[1]
    return description or strings.asked_by(", ".join(profiles[index].name for index, _ in route.requests))


def chip_text(standing: Standing, name: str, strings: Strings) -> str:
    """`Home holds`, `Office waits`."""
    match standing:
        case Standing.HOLDS:
            return strings.chip_holds(name)
        case Standing.WAITS:
            return strings.chip_waits(name)
        case Standing.BLOCKED:
            return strings.chip_blocked(name)
        case Standing.FAILED:
            return strings.chip_failed(name)
        case Standing.PENDING:
            return strings.chip_pending(name)


def why(route: BayRoute, profiles: ProfileSet, strings: Strings) -> str:
    """The sentence under the page: who holds the route and who waits behind it."""
    name = route_title(route.prefix, strings)[0]
    if route.holder is None:
        return strings.route_not_held(name)
    holder = profiles[route.holder].name
    waiting = [profiles[index].name for index, standing in route.requests if index != route.holder and standing is Standing.WAITS]
    if not waiting:
        return strings.route_held_by(name, holder)
    return strings.route_held_with_waiting(name, holder, " · ".join(waiting))


def state_line(profile: Profile, rank: int, strings: Strings) -> str:
    """Under a tunnel's name: its priority and how it stands. `rank` counts from 1."""
    match ProfileState(profile.state):
        case ProfileState.CONNECTED:
            held, asked = holds(profile)
            return strings.priority_holds(rank, held, asked)
        case ProfileState.CONNECTING | ProfileState.RECONNECTING:
            return strings.priority_connecting(rank)
        case ProfileState.DISCONNECTING:
            return strings.priority_disconnecting(rank)
        case ProfileState.AWAITING_CREDENTIALS:
            return strings.priority_needs_credentials(rank)
        case ProfileState.FAILED:
            return profile.last_error or strings.priority_failed(rank)
        case _:
            return strings.priority_not_connected(rank)


def caption(profile: Profile, presenter: Presenter) -> str:
    """Under a profile's name in the sidebar: `WireGuard · Holds 3 of 3 routes` while connected, else its state."""
    if ProfileState(profile.state) is not ProfileState.CONNECTED:
        return presenter.profile_subtitle(profile)
    held, asked = holds(profile)
    return f"{presenter.kind_text(profile.kind)} · {presenter.strings.holds_routes(held, asked)}"
