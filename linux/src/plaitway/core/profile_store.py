"""The daemon's profiles as the UI sees them.

The daemon is the only source of truth: every command sends a request and the
result arrives through the watch, so the store never guesses a state.

State is kept under a lock and read from any thread; `changed` tells the UI
thread to read it again. The commands block, so a view calls them from a job
(TaskRunner); they raise DaemonFailure.
"""

from __future__ import annotations

import enum
import logging
import threading
from collections.abc import Callable

from ..client.daemon_client import DaemonClient
from ..client.errors import DaemonFailure, FailureKind
from ..client.profile_set import ProfileSet
from ..client.types import (
    CredentialKind,
    Credentials,
    DaemonInfo,
    Diagnostics,
    Profile,
    ProfileEvent,
    ProfileImport,
    ProfileSettings,
    ProfileState,
)
from .observable import Notifier
from .ports import CredentialPrompt, CredentialStore, CredentialStoreError, UiScheduler
from .tasks import TaskRunner

log = logging.getLogger(__name__)

SAVED_KINDS = (CredentialKind.USER_PASSWORD, CredentialKind.KEY_PASSPHRASE)


class Connection(enum.Enum):
    CONNECTING = "connecting"
    CONNECTED = "connected"
    UNAVAILABLE = "unavailable"


class ProfileStore:
    def __init__(
        self,
        client: DaemonClient,
        credentials: CredentialStore,
        scheduler: UiScheduler,
        runner: TaskRunner,
    ) -> None:
        self.client = client
        #: Called, from any thread, when what the person typed could not be kept;
        #: the connection goes on without it.
        self.on_credentials_not_saved: Callable[[CredentialStoreError], None] | None = None
        self.changed = Notifier(scheduler)
        self._credentials = credentials
        self._runner = runner
        self._lock = threading.RLock()
        self._watch = None
        self._connection = Connection.CONNECTING
        self._failure: DaemonFailure | None = None
        self._profiles = ProfileSet()
        self._daemon_info: DaemonInfo | None = None
        self._prompts: list[CredentialPrompt] = []
        # Profiles that were sent credentials during the current connection
        # attempt: asking again means the daemon refused them.
        self._answered: set[str] = set()
        # Profiles whose credential request is being looked up in the credential store.
        self._resolving: set[str] = set()

    # MARK: State

    @property
    def connection(self) -> Connection:
        with self._lock:
            return self._connection

    @property
    def failure(self) -> DaemonFailure | None:
        """Why the daemon is unavailable, while it is."""
        with self._lock:
            return self._failure

    @property
    def profiles(self) -> ProfileSet:
        """Last known profiles in priority order, highest first. Kept while the
        daemon is unavailable so the UI does not empty out on a reconnect."""
        with self._lock:
            return self._profiles

    def profile(self, profile_id: str) -> Profile | None:
        return self.profiles.get(profile_id)

    @property
    def daemon_info(self) -> DaemonInfo | None:
        """What the daemon said about itself on the latest connection."""
        with self._lock:
            return self._daemon_info

    @property
    def credential_prompts(self) -> list[CredentialPrompt]:
        """Profiles waiting for the user to type credentials."""
        with self._lock:
            return list(self._prompts)

    def start(self) -> None:
        """Follows the daemon until `stop()`."""
        with self._lock:
            if self._watch is None:
                self._watch = self.client.watch_profiles(_Listener(self))

    def stop(self) -> None:
        with self._lock:
            watch, self._watch = self._watch, None
        if watch is not None:
            watch.cancel()

    # MARK: Commands

    def set_enabled(self, enabled: bool, profile_id: str) -> None:
        """Connects or disconnects a profile. Enabling a failed profile retries it."""
        self.client.set_profile_enabled(profile_id, enabled)

    def import_profile(
        self,
        content: bytes,
        source_filename: str,
        name: str = "",
        settings: ProfileSettings | None = None,
        credentials: Credentials | None = None,
    ) -> ProfileImport:
        """Stores a new profile, last in priority order.

        credentials: the user name and password the profile came with
            (ProfileImporter); they are saved for the new profile, so that its
            first connection does not ask.
        """
        result = self.client.import_profile(content, source_filename, name, settings)
        if credentials is not None:
            self._save_credentials(credentials, result.profile.id, CredentialKind.USER_PASSWORD)
        return result

    def update_profile(self, profile_id: str, name: str | None = None, settings: ProfileSettings | None = None) -> None:
        """Changes the name, the settings or both. A connected profile keeps running
        with the settings it was started with."""
        self.client.update_profile(profile_id, name, settings)

    def profile_content(self, profile_id: str) -> str:
        return self.client.get_profile_content(profile_id)

    def update_profile_content(self, profile_id: str, content: str, reconnect: bool = False) -> ProfileImport:
        return self.client.update_profile_content(profile_id, content, reconnect)

    def delete_profile(self, profile_id: str) -> None:
        """Disconnects the profile and removes it, with the credentials saved for it."""
        self.client.delete_profile(profile_id)
        # The watch reports the removal too, but it may be down right now, and
        # the keyring would keep the password of a profile that no longer exists.
        self.forget_saved_credentials(profile_id)

    def reorder(self, ids: list[str]) -> None:
        """`ids` lists every profile, highest priority first."""
        self.client.reorder_profiles(ids)

    def fetch_diagnostics(self) -> Diagnostics:
        return self.client.get_diagnostics()

    def resync(self) -> None:
        self.client.resync()

    def remove_stale_route(self, key: str) -> None:
        self.client.remove_stale_route(key)

    # MARK: Credentials
    #
    # A profile that asks is answered from the credential store when it has an
    # answer, so the user types each password once. The dialog appears when
    # there is none, and again when the daemon refuses what was sent.

    def provide_credentials(self, profile_id: str, username: str = "", password: str = "") -> None:
        """Sends what the user typed, and keeps it for the next request."""
        with self._lock:
            prompt = next((p for p in self._prompts if p.profile_id == profile_id), None)
        if prompt is None:
            raise DaemonFailure(FailureKind.REJECTED, "the profile is not waiting for credentials")
        credentials = Credentials(username=username, password=password)
        self._save_credentials(credentials, profile_id, prompt.kind)
        self._send(credentials, profile_id, prompt.kind)
        with self._lock:
            self._prompts = [p for p in self._prompts if p.profile_id != profile_id]
        self.changed.notify()

    def cancel_credentials(self, profile_id: str) -> None:
        """The user gave up on the dialog: stop connecting the profile."""
        with self._lock:
            self._prompts = [p for p in self._prompts if p.profile_id != profile_id]
            self._answered.discard(profile_id)
        self.changed.notify()
        self.set_enabled(False, profile_id)

    def has_saved_credentials(self, profile_id: str) -> bool:
        """Whether the credential store holds an answer for the profile, of either kind."""
        found = False
        for kind in SAVED_KINDS:
            try:
                found = self._credentials.has(profile_id, kind) or found
            except CredentialStoreError:
                log.exception("could not look up the saved credentials of %s", profile_id)
        return found

    def forget_saved_credentials(self, profile_id: str) -> None:
        for kind in SAVED_KINDS:
            self._remove_saved(profile_id, kind)

    def _save_credentials(self, credentials: Credentials, profile_id: str, kind: CredentialKind) -> None:
        """Connecting matters more than remembering, so a failure does not stop the
        connection; it is reported, and the next request asks again."""
        try:
            self._credentials.save(credentials, profile_id, kind)
        except CredentialStoreError as error:
            log.error("could not save the credentials of %s: %s", profile_id, error)
            if self.on_credentials_not_saved is not None:
                self.on_credentials_not_saved(error)

    def _remove_saved(self, profile_id: str, kind: CredentialKind) -> None:
        try:
            self._credentials.remove(profile_id, kind)
        except CredentialStoreError:
            log.exception("could not remove the saved credentials of %s", profile_id)

    def _saved(self, profile_id: str, kind: CredentialKind) -> Credentials | None:
        try:
            return self._credentials.lookup(profile_id, kind)
        except CredentialStoreError:
            # Including a keyring that stays locked: the dialog asks instead.
            log.exception("could not read the saved credentials of %s", profile_id)
            return None

    def _send(self, credentials: Credentials, profile_id: str, kind: CredentialKind) -> None:
        with self._lock:
            self._answered.add(profile_id)
        try:
            self.client.provide_credentials(profile_id, kind, credentials.username, credentials.password)
        except Exception:
            with self._lock:
                self._answered.discard(profile_id)
            raise

    # MARK: Watching (the listener runs on the watch's thread)

    def _apply(self, event: ProfileEvent) -> None:
        which = event.WhichOneof("event")
        refresh_info = False
        forgotten: str | None = None
        with self._lock:
            if which == "snapshot":
                self._profiles = ProfileSet(event.snapshot.profiles)
                self._connection = Connection.CONNECTED
                self._failure = None
                for profile in event.snapshot.profiles:
                    self._review_credential_request(profile)
                existing = set(self._profiles.ids)
                self._prompts = [p for p in self._prompts if p.profile_id in existing]
                refresh_info = True
            elif which == "changed":
                self._profiles = self._profiles.with_changed(event.changed)
                self._review_credential_request(event.changed)
            elif which == "removed":
                forgotten = event.removed
                self._profiles = self._profiles.without(forgotten)
                self._prompts = [p for p in self._prompts if p.profile_id != forgotten]
                self._answered.discard(forgotten)
            else:
                return
        if refresh_info:
            self._runner.run(self._read_daemon_info)
        if forgotten is not None:
            self._runner.run(lambda: self.forget_saved_credentials(forgotten))
        self.changed.notify()

    def _mark_unavailable(self, failure: DaemonFailure) -> None:
        with self._lock:
            self._connection = Connection.UNAVAILABLE
            self._failure = failure
            # Whatever the engines were asking for is gone with the daemon; the
            # next snapshot says what is still asked.
            self._prompts = []
            self._answered = set()
        log.error("daemon unavailable: %s", failure)
        self.changed.notify()

    def _read_daemon_info(self) -> None:
        try:
            info = self.client.get_daemon_info()
        except DaemonFailure as failure:
            log.error("could not read the daemon's version: %s", failure)
            return
        with self._lock:
            self._daemon_info = info
        self.changed.notify()

    def _review_credential_request(self, profile: Profile) -> None:
        """Called for every profile the daemon reports, with the lock held."""
        if profile.state != ProfileState.AWAITING_CREDENTIALS:
            self._prompts = [p for p in self._prompts if p.profile_id != profile.id]
            # CONNECTING is the daemon checking what was sent; only an attempt
            # that ended starts the next one with a clean slate.
            if profile.state in (ProfileState.CONNECTED, ProfileState.DISCONNECTED, ProfileState.FAILED):
                self._answered.discard(profile.id)
            return

        kind = CredentialKind(profile.credential_request.kind)
        if any(p.profile_id == profile.id and p.kind == kind for p in self._prompts):
            return
        # The keyring may be slow to answer; one lookup per profile at a time.
        if profile.id in self._resolving:
            return
        self._resolving.add(profile.id)
        rejected = profile.id in self._answered or bool(profile.last_error)
        self._runner.run(lambda: self._resolve_request(profile, kind, rejected))

    def _resolve_request(self, profile: Profile, kind: CredentialKind, rejected: bool) -> None:
        """Answers the request from the credential store, or asks the user."""
        try:
            if rejected:
                self._remove_saved(profile.id, kind)
                with self._lock:
                    self._answered.discard(profile.id)
                self._show_prompt(profile, kind, rejected=True)
                return

            saved = self._saved(profile.id, kind)
            # The keyring took its time; the profile may have been answered, stopped or deleted meanwhile.
            current = self.profile(profile.id)
            if current is None or current.state != ProfileState.AWAITING_CREDENTIALS:
                return
            if saved is None:
                self._show_prompt(current, kind, rejected=False)
                return
            try:
                self._send(saved, profile.id, kind)
            except DaemonFailure as failure:
                # Not a refusal of the credentials: the daemon could not take them.
                log.error("could not answer the credential request of %s: %s", profile.id, failure)
                latest = self.profile(profile.id)
                if latest is not None and latest.state == ProfileState.AWAITING_CREDENTIALS:
                    self._show_prompt(current, kind, rejected=False)
        finally:
            with self._lock:
                self._resolving.discard(profile.id)

    def _show_prompt(self, profile: Profile, kind: CredentialKind, *, rejected: bool) -> None:
        prompt = CredentialPrompt(profile.id, kind, rejected, profile.last_error)
        with self._lock:
            self._prompts = [p for p in self._prompts if p.profile_id != profile.id] + [prompt]
        self.changed.notify()


class _Listener:
    def __init__(self, store: ProfileStore) -> None:
        self._store = store

    def on_event(self, event: ProfileEvent) -> None:
        self._store._apply(event)

    def on_outage(self, failure: DaemonFailure) -> None:
        self._store._mark_unavailable(failure)
