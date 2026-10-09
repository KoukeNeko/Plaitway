"""The two server streams, kept running through outages of the daemon."""

from __future__ import annotations

import collections
import logging
import threading
from collections.abc import Callable
from typing import Protocol

import grpc

from .backoff import BackoffPolicy
from .errors import DaemonFailure, FailureKind
from .types import LogLine, ProfileEvent

log = logging.getLogger(__name__)

# What the log watch remembers of the lines it delivered. The daemon keeps 1000
# lines and sends at most that many again when the stream restarts.
REPLAY_WINDOW = 2000


class ProfileWatchListener(Protocol):
    """Called on the watch's thread."""

    def on_event(self, event: ProfileEvent) -> None:
        """A snapshot (first on every connection), a changed profile or a removed id."""

    def on_outage(self, failure: DaemonFailure) -> None:
        """The stream broke or could not be opened. Once per outage; again only
        when the reason changes (the daemon went away, then refuses us)."""


class LogWatchListener(Protocol):
    """Called on the watch's thread."""

    def on_line(self, line: LogLine) -> None: ...

    def on_outage(self, failure: DaemonFailure) -> None: ...

    def on_ended(self, failure: DaemonFailure) -> None:
        """The log cannot be followed (the profile is gone): the watch has stopped."""


class Watch:
    """A running stream. `cancel()` ends the call at the daemon and the thread."""

    def __init__(self, name: str, run: Callable[[Watch], None]) -> None:
        self._stopped = threading.Event()
        self._call_lock = threading.Lock()
        self._call: grpc.Call | None = None
        self._thread = threading.Thread(target=run, args=(self,), name=name, daemon=True)

    def start(self) -> Watch:
        self._thread.start()
        return self

    @property
    def stopped(self) -> bool:
        return self._stopped.is_set()

    def cancel(self) -> None:
        self._stopped.set()
        with self._call_lock:
            call = self._call
        if call is not None:
            call.cancel()

    def join(self, timeout: float | None = None) -> None:
        self._thread.join(timeout)

    def is_alive(self) -> bool:
        return self._thread.is_alive()

    # Used by the thread that runs the stream.

    def attach(self, call: grpc.Call) -> None:
        with self._call_lock:
            self._call = call
        if self.stopped:
            call.cancel()

    def pause(self, seconds: float) -> None:
        """Waits, or returns early when cancelled."""
        self._stopped.wait(seconds)


class OutageReporter:
    """Tells the listener about an outage once, and again when its reason changes."""

    def __init__(self, report: Callable[[DaemonFailure], None]) -> None:
        self._report = report
        self._reported: FailureKind | None = None

    def failed(self, failure: DaemonFailure) -> None:
        if failure.kind != self._reported:
            self._reported = failure.kind
            self._report(failure)

    def recovered(self) -> None:
        self._reported = None


def run_with_retries(
    watch: Watch,
    backoff: BackoffPolicy,
    attempt: Callable[[Callable[[], None]], DaemonFailure | None],
    outage: Callable[[DaemonFailure], None],
) -> None:
    """Runs `attempt` until the watch is cancelled or `attempt` returns None.

    `attempt` opens the stream, reads it until it ends, and calls the function
    it is given as soon as the stream proved to work. It returns why it ended.
    """
    reporter = OutageReporter(outage)
    failures = 0

    def connected() -> None:
        nonlocal failures
        failures = 0
        reporter.recovered()

    while not watch.stopped:
        failure = attempt(connected)
        if failure is None or watch.stopped:
            return
        reporter.failed(failure)
        watch.pause(backoff.delay(failures))
        failures += 1


class ReplayFilter:
    """Drops the lines of a restarted log stream that were delivered before.

    A restarted WatchLogs sends the buffered tail again. The lines of the tail
    come first and are a part of what was delivered, so each is skipped as long
    as it is one of the remembered lines; the first one that is not ends the
    replay and everything from there is new. A daemon that restarted has a log
    of its own, whose lines are all new.
    """

    def __init__(self, window: int = REPLAY_WINDOW) -> None:
        self._recent: collections.deque[tuple] = collections.deque(maxlen=window)
        self._skippable: collections.Counter | None = None

    @staticmethod
    def _key(line: LogLine) -> tuple:
        time = (line.time.seconds, line.time.nanos) if line.HasField("time") else None
        return (time, line.level, line.text)

    def restart(self) -> None:
        self._skippable = collections.Counter(self._recent) if self._recent else None

    def accepts(self, line: LogLine) -> bool:
        key = self._key(line)
        if self._skippable is not None:
            if self._skippable[key] > 0:
                self._skippable[key] -= 1
                return False
            self._skippable = None
        self._recent.append(key)
        return True
