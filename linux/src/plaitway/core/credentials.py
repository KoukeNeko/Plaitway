from __future__ import annotations

import threading

from ..client.types import CredentialKind, Credentials
from .ports import CredentialStoreError

__all__ = ["CredentialStoreError", "InMemoryCredentialStore"]


class InMemoryCredentialStore:
    """For tests, and for a development daemon: its credential requests are not
    answered from the keyring, and nothing it is given is kept beyond the process."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._items: dict[tuple[str, int], Credentials] = {}

    def lookup(self, profile_id: str, kind: CredentialKind) -> Credentials | None:
        with self._lock:
            return self._items.get((profile_id, int(kind)))

    def has(self, profile_id: str, kind: CredentialKind) -> bool:
        with self._lock:
            return (profile_id, int(kind)) in self._items

    def save(self, credentials: Credentials, profile_id: str, kind: CredentialKind) -> None:
        with self._lock:
            self._items[(profile_id, int(kind))] = credentials

    def remove(self, profile_id: str, kind: CredentialKind) -> None:
        with self._lock:
            self._items.pop((profile_id, int(kind)), None)
