"""Doubles for the ports, for the tests and for tools/capture_ui.py."""

from __future__ import annotations

import heapq
import itertools
import threading
import time
from collections.abc import Callable

from .import_report import ImportReport
from .ports import (
    Confirmation,
    CredentialPrompt,
    HelperRegistration,
    HelperServiceError,
    Platform,
    QuitAnswer,
)


class FakeScheduler:
    """A UI thread that is the caller's: what is posted runs when `run_pending`
    or `wait_until` is called, on the calling thread, which is what the test
    thread is."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._posted: list[Callable[[], None]] = []
        self._timers: list[tuple[float, int, Callable[[], None]]] = []
        self._counter = itertools.count()
        self._cancelled: set[int] = set()

    def post(self, fn: Callable[[], None]) -> None:
        with self._lock:
            self._posted.append(fn)

    def call_later(self, seconds: float, fn: Callable[[], None]) -> Callable[[], None]:
        number = next(self._counter)
        with self._lock:
            heapq.heappush(self._timers, (time.monotonic() + seconds, number, fn))

        def cancel() -> None:
            with self._lock:
                self._cancelled.add(number)

        return cancel

    def run_pending(self) -> int:
        """Runs what is posted and what is due; returns how many calls it made."""
        calls = 0
        while True:
            with self._lock:
                due = []
                while self._timers and self._timers[0][0] <= time.monotonic():
                    _, number, fn = heapq.heappop(self._timers)
                    if number in self._cancelled:
                        self._cancelled.discard(number)
                    else:
                        due.append(fn)
                batch, self._posted = self._posted, []
            batch += due
            if not batch:
                return calls
            for fn in batch:
                fn()
                calls += 1

    def wait_until(self, condition: Callable[[], object], what: str = "the condition", timeout: float = 10.0):
        """Runs the UI thread's work until `condition` holds; fails after `timeout`."""
        deadline = time.monotonic() + timeout
        while True:
            self.run_pending()
            result = condition()
            if result:
                return result
            if time.monotonic() > deadline:
                raise TimeoutError(f"timed out waiting for {what}")
            time.sleep(0.01)

    def settle(self, seconds: float = 0.2) -> None:
        """Runs the UI thread's work for a while, for what must not happen."""
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            self.run_pending()
            time.sleep(0.01)


class FakeClipboard:
    def __init__(self) -> None:
        self.text: str | None = None

    def set_text(self, text: str) -> None:
        self.text = text


class FakeFilePicker:
    def __init__(self) -> None:
        self.requests: list[Callable[[list[str]], None]] = []

    def choose_profiles(self, on_chosen: Callable[[list[str]], None]) -> None:
        self.requests.append(on_chosen)

    def choose(self, paths: list[str]) -> None:
        """The person picks `paths` in the oldest open picker."""
        self.requests.pop(0)(paths)


class FakeHelperService:
    def __init__(self, registration: HelperRegistration = HelperRegistration.RUNNING) -> None:
        self._registration = registration
        self.state_after_start = HelperRegistration.RUNNING
        self.calls: list[str] = []
        self.fails = False

    @property
    def current(self) -> HelperRegistration:
        return self._registration

    @current.setter
    def current(self, value: HelperRegistration) -> None:
        self._registration = value

    def registration(self) -> HelperRegistration:
        return self._registration

    def start(self) -> None:
        self.calls.append("start")
        if self.fails:
            raise HelperServiceError("Interactive authentication required.")
        self._registration = self.state_after_start

    def restart(self) -> None:
        self.calls.append("restart")
        if self.fails:
            raise HelperServiceError("Interactive authentication required.")
        self._registration = self.state_after_start


class FakeLoginItem:
    def __init__(self) -> None:
        self.is_enabled = False

    def set_enabled(self, enabled: bool) -> None:
        self.is_enabled = enabled


class FakeDialogs:
    """Records what is shown. A confirmation is answered at once from `answers`
    (yes while there is none left)."""

    def __init__(self) -> None:
        self.alerts: list[tuple[str, str]] = []
        self.confirmations: list[Confirmation] = []
        self.answers: list[bool] = []
        self.import_reports: list[ImportReport] = []
        self.credential_requests: dict[str, "CredentialRequest"] = {}
        self.dismissed: list[str] = []

    def show_alert(self, title: str, message: str) -> None:
        self.alerts.append((title, message))

    def confirm(self, confirmation: Confirmation, on_answer: Callable[[bool], None]) -> None:
        self.confirmations.append(confirmation)
        on_answer(self.answers.pop(0) if self.answers else True)

    def show_import_report(self, report: ImportReport) -> None:
        self.import_reports.append(report)

    def ask_credentials(self, prompt: CredentialPrompt, profile_name: str, on_submit, on_cancel) -> None:
        self.credential_requests[prompt.profile_id] = CredentialRequest(prompt, profile_name, on_submit, on_cancel)

    def dismiss_credentials(self, profile_id: str) -> None:
        self.credential_requests.pop(profile_id, None)
        self.dismissed.append(profile_id)


class CredentialRequest:
    def __init__(self, prompt: CredentialPrompt, profile_name: str, on_submit, on_cancel) -> None:
        self.prompt = prompt
        self.profile_name = profile_name
        self._on_submit = on_submit
        self._on_cancel = on_cancel

    def submit(self, username: str, password: str) -> None:
        self._on_submit(username, password)

    def cancel(self) -> None:
        self._on_cancel()


class FakeQuitPrompts:
    def __init__(self) -> None:
        self.answer = QuitAnswer.QUIT
        self.discard_edits = True
        self.asked: list[str] = []

    def ask_quit(self, on_answer: Callable[[QuitAnswer], None]) -> None:
        self.asked.append("quit")
        on_answer(self.answer)

    def confirm_discarding_edits(self, on_answer: Callable[[bool], None]) -> None:
        self.asked.append("discard edits")
        on_answer(self.discard_edits)


def fake_platform(scheduler: FakeScheduler | None = None) -> Platform:
    return Platform(
        scheduler=scheduler or FakeScheduler(),
        clipboard=FakeClipboard(),
        file_picker=FakeFilePicker(),
        helper=FakeHelperService(),
        login_item=FakeLoginItem(),
        dialogs=FakeDialogs(),
        quit_prompts=FakeQuitPrompts(),
    )

