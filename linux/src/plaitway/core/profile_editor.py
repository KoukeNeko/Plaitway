from __future__ import annotations

import enum
import threading
from collections.abc import Callable
from dataclasses import dataclass

from ..client.config_diagnostic import ConfigDiagnostic
from ..client.errors import DaemonFailure
from ..client.secret_mask import DuplicatedPlaceholder, Restoration, SecretMask
from ..client.types import ImportWarning
from ..l10n.strings import Strings
from .observable import Notifier
from .profile_store import ProfileStore
from .ports import UiScheduler
from .tasks import Outcome, TaskRunner
from .user_message import user_message


class Phase(enum.Enum):
    LOADING = "loading"
    READY = "ready"
    # The text cannot be read: the person is not an administrator, or the daemon is away.
    UNAVAILABLE = "unavailable"


@dataclass(frozen=True)
class SaveResult:
    saved: bool
    # Why it failed, when the daemon did not refuse the text but could not take it.
    error: Exception | None = None


class ProfileEditor:
    """The text of one profile while it is edited: what the editor shows, what
    the daemon holds, and what the daemon said about the last attempt to store
    it. It lives in the app model, so that a half-made edit survives a visit to
    another profile.

    The secrets (private keys, inline key blocks) are kept off the screen: the
    editor shows a placeholder in their place until the person asks to see
    them, and SecretMask puts them back at save time.

    `text` and the toggles are for the UI thread. `load` and `save` work on a
    thread of their own and report on the UI thread.
    """

    def __init__(
        self,
        profile_id: str,
        kind: int,
        store: ProfileStore,
        strings: Strings,
        scheduler: UiScheduler,
        runner: TaskRunner,
    ) -> None:
        self.profile_id = profile_id
        self.kind = kind
        self.changed = Notifier(scheduler)
        self._store = store
        self._strings = strings
        self._runner = runner
        self._lock = threading.RLock()
        self.phase = Phase.LOADING
        self.unavailable_message = ""
        # What the editor shows and the person changes.
        self.text = ""
        self.shows_secrets = False
        # What the daemon refused in the last save; its line is a line of `text`.
        self.diagnostic: ConfigDiagnostic | None = None
        # What the daemon changed in the text it stored.
        self.warnings: list[ImportWarning] = []
        # The last save did not restart the profile, which is on: it runs with the old text.
        self.runs_old_text = False
        self.is_saving = False
        # The text the daemon holds, secrets included.
        self._stored = ""
        self._mask = SecretMask("", kind)

    # MARK: State

    def _restored_text(self) -> str | None:
        """The text with the secrets back in it; None while a placeholder stands twice."""
        if self.shows_secrets:
            return self.text
        try:
            return self._mask.restore(self.text).text
        except DuplicatedPlaceholder:
            return None

    @property
    def is_dirty(self) -> bool:
        """Whether the person changed something. A text that cannot be restored is changed by definition."""
        with self._lock:
            return self.phase is Phase.READY and self._restored_text() != self._stored

    # MARK: Loading

    def load(self, done: Callable[[], None] | None = None) -> None:
        """Reads the profile text, unless there are edits that a read would throw away."""
        if self.is_dirty:
            return

        def finished(outcome: Outcome) -> None:
            with self._lock:
                if outcome.ok:
                    self._stored = outcome.value
                    self.diagnostic = None
                    self._show(self._stored)
                    self.phase = Phase.READY
                elif self.phase is not Phase.READY:
                    # An edit in progress stays on screen; a failed first read says why.
                    self.phase = Phase.UNAVAILABLE
                    self.unavailable_message = user_message(outcome.error, self._strings)
            self.changed.notify()
            if done is not None:
                done()

        self._runner.run(lambda: self._store.profile_content(self.profile_id), finished)

    # MARK: Secrets

    def toggle_secrets(self) -> None:
        """Shows the secrets, or hides them again with whatever the person typed in the meantime."""
        with self._lock:
            if self.phase is not Phase.READY:
                return
            # A mark is a line of the text as it was shown, and showing or hiding a key block changes the lines.
            self.diagnostic = None
            if self.shows_secrets:
                self._mask = SecretMask(self.text, self.kind)
                self.text = self._mask.display_text
                self.shows_secrets = False
            else:
                try:
                    self.text = self._mask.restore(self.text).text
                    self.shows_secrets = True
                except DuplicatedPlaceholder as duplicate:
                    # A secret cannot be shown in two places.
                    self.diagnostic = self._duplicate(duplicate)
        self.changed.notify()

    def hide_secrets(self) -> None:
        """Hides the secrets again, with the edits made while they were shown. The
        editor outlives the page, and the keys must not be on screen when the
        person comes back to it."""
        if self.shows_secrets:
            self.toggle_secrets()

    def note_restart(self) -> None:
        """The profile connected again: it runs the text that is stored now."""
        if self.runs_old_text:
            self.runs_old_text = False
            self.changed.notify()

    def revert(self) -> None:
        with self._lock:
            self._show(self._stored)
            self.diagnostic = None
        self.changed.notify()

    def set_text(self, text: str) -> None:
        """The person edited the text."""
        self.text = text

    # MARK: Saving

    def save(self, reconnect: bool, is_on: bool, done: Callable[[SaveResult], None] | None = None) -> None:
        """Stores the text. When the daemon refuses it, `diagnostic` says why.

        reconnect: restarts the profile with the new text.
        is_on: the profile is switched on, so that it keeps running with the old
            text unless restarted.
        done: called on the UI thread with how it went.
        """
        with self._lock:
            if self.phase is not Phase.READY or self.is_saving:
                self._report(done, SaveResult(saved=False))
                return
            restoration: Restoration | None = None
            if self.shows_secrets:
                content = self.text
            else:
                try:
                    restoration = self._mask.restore(self.text)
                except DuplicatedPlaceholder as duplicate:
                    self.diagnostic = self._duplicate(duplicate)
                    self.changed.notify()
                    self._report(done, SaveResult(saved=False))
                    return
                content = restoration.text
            self.is_saving = True
        self.changed.notify()

        def job() -> str:
            self._store.update_profile_content(self.profile_id, content, reconnect)
            # The daemon may have stripped something from what it stored: show what it holds.
            try:
                return self._store.profile_content(self.profile_id)
            except DaemonFailure:
                return content

        def finished(outcome: Outcome) -> None:
            result = SaveResult(saved=outcome.ok)
            with self._lock:
                self.is_saving = False
                if outcome.ok:
                    self.runs_old_text = is_on and not reconnect
                    self.diagnostic = None
                    self._stored = outcome.value
                    self._show(self._stored)
                elif (rejection := ConfigDiagnostic.from_error(outcome.error)) is not None:
                    line = rejection.line
                    if line is not None and restoration is not None:
                        line = restoration.display_line(line)
                    self.diagnostic = ConfigDiagnostic(line, rejection.message)
                else:
                    result = SaveResult(saved=False, error=outcome.error)
            self.changed.notify()
            self._report(done, result)

        self._runner.run(job, finished)

    @staticmethod
    def _report(done: Callable[[SaveResult], None] | None, result: SaveResult) -> None:
        if done is not None:
            done(result)

    # MARK: Helpers

    def _show(self, restored: str) -> None:
        """Shows `restored` as the editor's text, with the secrets hidden unless they are shown."""
        if self.shows_secrets:
            self.text = restored
        else:
            self._mask = SecretMask(restored, self.kind)
            self.text = self._mask.display_text

    def _duplicate(self, duplicate: DuplicatedPlaceholder) -> ConfigDiagnostic:
        return ConfigDiagnostic(duplicate.line, self._strings.secret_appears_more_than_once(duplicate.number))
