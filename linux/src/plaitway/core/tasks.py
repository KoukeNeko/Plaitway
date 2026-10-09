"""Work that blocks, off the UI thread, with its result back on it."""

from __future__ import annotations

import logging
import threading
from collections.abc import Callable
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
from typing import Generic, TypeVar

from .ports import Cancel, UiScheduler

log = logging.getLogger(__name__)

T = TypeVar("T")


@dataclass(frozen=True)
class Outcome(Generic[T]):
    """What a job returned, or the exception it raised."""

    value: T | None = None
    error: Exception | None = None

    @property
    def ok(self) -> bool:
        return self.error is None


class TaskRunner:
    """Runs jobs on worker threads. `on_done` is called on the UI thread."""

    def __init__(self, scheduler: UiScheduler, workers: int = 8) -> None:
        self._scheduler = scheduler
        self._pool = ThreadPoolExecutor(max_workers=workers, thread_name_prefix="plaitway-task")

    def run(self, job: Callable[[], T], on_done: Callable[[Outcome[T]], None] | None = None) -> None:
        def work() -> None:
            try:
                outcome = Outcome(value=job())
            except Exception as error:  # reported to the caller, which decides what it means
                outcome = Outcome(error=error)
            if on_done is not None:
                self._scheduler.post(lambda: on_done(outcome))
            elif not outcome.ok:
                log.error("a background job failed", exc_info=outcome.error)

        self._pool.submit(work)

    def shutdown(self) -> None:
        self._pool.shutdown(wait=False, cancel_futures=True)


class Ticker:
    """Calls a function every `interval` seconds on a thread of its own, until stopped.
    The first call is at once."""

    def __init__(self, interval: float, fn: Callable[[], None], name: str = "plaitway-ticker") -> None:
        self._interval = interval
        self._fn = fn
        self._stop = threading.Event()
        self._thread = threading.Thread(target=self._run, name=name, daemon=True)

    def start(self) -> Ticker:
        self._thread.start()
        return self

    def stop(self) -> None:
        self._stop.set()

    def _run(self) -> None:
        while not self._stop.is_set():
            try:
                self._fn()
            except Exception:
                log.exception("a periodic job failed")
            self._stop.wait(self._interval)


def cancel_all(cancels: list[Cancel]) -> None:
    for cancel in cancels:
        cancel()
    cancels.clear()
