"""The credential store in the Secret Service (GNOME Keyring, KWallet) through libsecret.

One item per profile and kind, with the attributes `profile_id` and `kind`. The
secret is the JSON the macOS Keychain item holds, so the two apps keep the same
thing: {"username": ..., "password": ...}.
"""

from __future__ import annotations

import json
import logging

import gi

gi.require_version("Secret", "1")
from gi.repository import Gio, GLib, Secret  # noqa: E402

from ..client.types import CredentialKind, Credentials  # noqa: E402
from ..core.ports import CredentialStoreError  # noqa: E402

log = logging.getLogger(__name__)

SCHEMA_NAME = "io.github.koukeneko.plaitway.credentials"
SCHEMA = Secret.Schema.new(
    SCHEMA_NAME,
    Secret.SchemaFlags.NONE,
    {"profile_id": Secret.SchemaAttributeType.STRING, "kind": Secret.SchemaAttributeType.STRING},
)

# D-Bus errors that say there is nobody to ask, as opposed to somebody who said no.
_UNAVAILABLE_CODES = (
    Gio.DBusError.SERVICE_UNKNOWN,
    Gio.DBusError.NAME_HAS_NO_OWNER,
    Gio.DBusError.NO_SERVER,
    Gio.DBusError.FILE_NOT_FOUND,
    Gio.DBusError.SPAWN_SERVICE_NOT_FOUND,
    Gio.DBusError.SPAWN_EXEC_FAILED,
    Gio.DBusError.TIMED_OUT,
    Gio.DBusError.DISCONNECTED,
)


def _is_unavailable(error: GLib.Error) -> bool:
    return any(error.matches(Gio.dbus_error_quark(), code) for code in _UNAVAILABLE_CODES)


def _call(operation: str, call):
    """Runs a libsecret call and words its failure."""
    try:
        return call()
    except GLib.Error as error:
        raise CredentialStoreError(
            f"Secret Service {operation} failed: {error.message}", unavailable=_is_unavailable(error)
        ) from error


def kind_name(kind: CredentialKind) -> str:
    return "key-passphrase" if kind == CredentialKind.KEY_PASSPHRASE else "user-password"


def _attributes(profile_id: str, kind: CredentialKind) -> dict[str, str]:
    return {"profile_id": profile_id, "kind": kind_name(kind)}


class SecretServiceStore:
    """The calls block, and the Secret Service may stop to ask for the keyring's
    password: they run on worker threads."""

    def lookup(self, profile_id: str, kind: CredentialKind) -> Credentials | None:
        value = _call("read", lambda: Secret.password_lookup_sync(SCHEMA, _attributes(profile_id, kind), None))
        if value is None:
            return None
        try:
            stored = json.loads(value)
            return Credentials(username=stored.get("username", ""), password=stored["password"])
        except (ValueError, KeyError, AttributeError) as error:
            raise CredentialStoreError(f"the saved credentials of {profile_id} are unreadable: {error}") from error

    def has(self, profile_id: str, kind: CredentialKind) -> bool:
        # Searching without loading the secrets needs no permission, and does not unlock a keyring.
        found = _call(
            "read",
            lambda: Secret.password_search_sync(SCHEMA, _attributes(profile_id, kind), Secret.SearchFlags.NONE, None),
        )
        return bool(found)

    def save(self, credentials: Credentials, profile_id: str, kind: CredentialKind) -> None:
        secret = json.dumps({"username": credentials.username, "password": credentials.password})
        label = f"Plaitway: {profile_id} ({kind_name(kind)})"
        stored = _call(
            "write",
            lambda: Secret.password_store_sync(
                SCHEMA, _attributes(profile_id, kind), Secret.COLLECTION_DEFAULT, label, secret, None
            ),
        )
        if not stored:
            raise CredentialStoreError("the Secret Service did not store the credentials")

    def remove(self, profile_id: str, kind: CredentialKind) -> None:
        _call("delete", lambda: Secret.password_clear_sync(SCHEMA, _attributes(profile_id, kind), None))
