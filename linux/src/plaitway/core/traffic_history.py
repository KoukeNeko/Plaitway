from __future__ import annotations

import threading
import time
from dataclasses import dataclass

from ..client.types import Profile, ProfileState

# Two minutes at the daemon's pace.
CAPACITY = 60
# A reading closer than this to the last one is a second event for the same counters.
MINIMUM_INTERVAL = 0.9
# The rate shown is the mean of the last few samples; one reading jumps too much to read.
SMOOTHING = 3
# Counters that have not moved for this long are a quiet tunnel, and a reading says so. Before
# that they are a reading of the same report as the last one: the daemon has not told us yet,
# and a sample of nothing in between would draw a dip, and a spike after it.
QUIET_AFTER = 3.0


@dataclass(frozen=True)
class Sample:
    time: float
    received: float  # bytes per second
    sent: float


@dataclass(frozen=True)
class Rate:
    received: float
    sent: float


class TrafficHistory:
    """How fast data moves through each connected profile, worked out here from
    the byte counters the daemon reports every couple of seconds; the daemon
    keeps no history."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._samples: dict[str, list[Sample]] = {}
        self._counters: dict[str, tuple[float, int, int]] = {}

    def record(self, profiles, now: float | None = None) -> None:
        now = time.time() if now is None else now
        connected = [profile for profile in profiles if profile.state == ProfileState.CONNECTED]
        with self._lock:
            connected_ids = {profile.id for profile in connected}
            for profile_id in [key for key in self._counters if key not in connected_ids]:
                # A reconnection starts the counters again, and so the graph.
                del self._counters[profile_id]
                self._samples.pop(profile_id, None)
            for profile in connected:
                self._record_profile(profile, now)

    def _record_profile(self, profile: Profile, now: float) -> None:
        received, sent = profile.status.rx_bytes, profile.status.tx_bytes
        previous = self._counters.get(profile.id)
        if previous is None:
            self._counters[profile.id] = (now, received, sent)
            return
        when, previous_received, previous_sent = previous
        elapsed = now - when
        # The reading stays as it was, so that the next one has a full interval to measure.
        if elapsed < MINIMUM_INTERVAL:
            return
        if (received, sent) == (previous_received, previous_sent) and elapsed < QUIET_AFTER:
            return
        self._counters[profile.id] = (now, received, sent)
        # A counter that went down was reset: the interval says nothing.
        if received < previous_received or sent < previous_sent:
            self._samples.pop(profile.id, None)
            return
        samples = self._samples.setdefault(profile.id, [])
        samples.append(Sample(now, (received - previous_received) / elapsed, (sent - previous_sent) / elapsed))
        del samples[: max(0, len(samples) - CAPACITY)]

    def samples(self, profile_id: str) -> list[Sample]:
        with self._lock:
            return list(self._samples.get(profile_id, ()))

    def rate(self, profile_id: str) -> Rate | None:
        """The current rates; None until there are two readings."""
        recent = self.samples(profile_id)[-SMOOTHING:]
        if not recent:
            return None
        return Rate(
            sum(sample.received for sample in recent) / len(recent),
            sum(sample.sent for sample in recent) / len(recent),
        )
