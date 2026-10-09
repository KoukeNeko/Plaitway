from __future__ import annotations

import threading
from dataclasses import dataclass

from ..client.daemon_client import DaemonClient
from ..client.errors import DaemonFailure
from ..client.types import LogLine
from .formatting import format_log_time
from .observable import Notifier
from .ports import UiScheduler
from .presentation import log_tag

# Lines kept; the daemon keeps 1000 itself, so this only bounds a long session.
CAPACITY = 2000
# Removing from the front in batches keeps a full buffer from shifting on every line.
TRIM_SLACK = 200


@dataclass(frozen=True)
class LogEntry:
    id: int
    time: float | None
    level: int
    text: str

    @property
    def time_text(self) -> str:
        """The time of day, or nothing when the line has none."""
        return format_log_time(self.time) if self.time is not None else ""

    @property
    def plain_text(self) -> str:
        """The line as it is copied: `12:00:01 INFO text`."""
        parts = [self.time_text, log_tag(self.level), self.text]
        return " ".join(part for part in parts if part)


class LogTail:
    """The live tail of one log: a profile's, or the daemon's own for an empty id."""

    def __init__(self, client: DaemonClient, profile_id: str, scheduler: UiScheduler) -> None:
        self.changed = Notifier(scheduler)
        self._client = client
        self._profile_id = profile_id
        self._lock = threading.Lock()
        self._entries: list[LogEntry] = []
        self._next_id = 0
        self._trimmed = 0
        self._ended = False
        self._watch = None

    def start(self) -> None:
        """Follows the log, through outages of the daemon, until `stop()`."""
        if self._watch is None:
            self._watch = self._client.watch_logs(self._profile_id, self)

    def stop(self) -> None:
        watch, self._watch = self._watch, None
        if watch is not None:
            watch.cancel()

    @property
    def entries(self) -> list[LogEntry]:
        with self._lock:
            return list(self._entries)

    def entries_after(self, last_id: int) -> tuple[list[LogEntry], bool]:
        """The entries newer than `last_id` (-1 for all), and whether older ones
        were dropped since: then what the caller shows is not a prefix of these."""
        with self._lock:
            if self._trimmed and last_id < self._trimmed - 1:
                return list(self._entries), True
            return [entry for entry in self._entries if entry.id > last_id], False

    @property
    def ended(self) -> bool:
        """The profile is gone: there will be no more lines."""
        with self._lock:
            return self._ended

    @property
    def plain_text(self) -> str:
        return "\n".join(entry.plain_text for entry in self.entries)

    # The listener of the watch, on its thread.

    def on_line(self, line: LogLine) -> None:
        self.append(line)

    def on_outage(self, failure: DaemonFailure) -> None:
        pass

    def on_ended(self, failure: DaemonFailure) -> None:
        with self._lock:
            self._ended = True
        self.changed.notify()

    def append(self, line: LogLine) -> None:
        with self._lock:
            when = line.time.seconds + line.time.nanos / 1e9 if line.HasField("time") else None
            self._entries.append(LogEntry(self._next_id, when, line.level, line.text))
            self._next_id += 1
            if len(self._entries) > CAPACITY + TRIM_SLACK:
                drop = len(self._entries) - CAPACITY
                del self._entries[:drop]
                self._trimmed = self._entries[0].id
        self.changed.notify()
