"""The profiles in priority order, and how a drag or a move changes the order."""

from __future__ import annotations

from collections.abc import Iterable, Sequence

from .types import Profile


def in_priority_order(profiles: Iterable[Profile]) -> list[Profile]:
    """Priority is the daemon's: lower wins. Events arrive one profile at a
    time, so profiles with equal priority (in the middle of a reorder) keep
    the position they have."""
    indexed = list(enumerate(profiles))
    indexed.sort(key=lambda item: (item[1].settings.priority, item[0]))
    return [profile for _, profile in indexed]


class ProfileSet:
    """The last known profiles, highest priority first. Immutable: each change
    makes a new set, so a reader never sees a half-applied event."""

    def __init__(self, profiles: Iterable[Profile] = ()) -> None:
        self._profiles = tuple(in_priority_order(profiles))

    def __iter__(self):
        return iter(self._profiles)

    def __len__(self) -> int:
        return len(self._profiles)

    def __bool__(self) -> bool:
        return bool(self._profiles)

    def __getitem__(self, index):
        return self._profiles[index]

    @property
    def ids(self) -> list[str]:
        return [profile.id for profile in self._profiles]

    def get(self, profile_id: str) -> Profile | None:
        return next((profile for profile in self._profiles if profile.id == profile_id), None)

    def with_changed(self, changed: Profile) -> ProfileSet:
        """The set with `changed` in place of the profile of its id, or added last."""
        profiles = list(self._profiles)
        for index, profile in enumerate(profiles):
            if profile.id == changed.id:
                profiles[index] = changed
                break
        else:
            profiles.append(changed)
        return ProfileSet(profiles)

    def without(self, profile_id: str) -> ProfileSet:
        return ProfileSet(profile for profile in self._profiles if profile.id != profile_id)


def moved(ids: Sequence[str], source: Iterable[int], destination: int) -> list[str]:
    """`ids` with the entries at the `source` positions moved to just before
    position `destination` of the list as it was, like a drag in a list."""
    moving = sorted(set(source))
    staying = [value for index, value in enumerate(ids) if index not in moving]
    insert_at = destination - sum(1 for index in moving if index < destination)
    return staying[:insert_at] + [ids[index] for index in moving] + staying[insert_at:]


def moved_by(ids: Sequence[str], profile_id: str, delta: int) -> list[str] | None:
    """`delta` -1 moves the profile up one place, +1 down; None at either end."""
    if profile_id not in ids:
        return None
    index = list(ids).index(profile_id)
    if not 0 <= index + delta < len(ids):
        return None
    result = list(ids)
    result[index], result[index + delta] = result[index + delta], result[index]
    return result
