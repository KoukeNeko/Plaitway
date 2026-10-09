from __future__ import annotations

import threading
from collections.abc import Callable

from .ports import Cancel, UiScheduler


class Notifier:
    """Tells the views that a model changed, on the UI thread.

    A model changes on any thread; `notify()` may be called from all of them.
    A burst of changes is one call of the listeners, which then read the model.
    """

    def __init__(self, scheduler: UiScheduler) -> None:
        self._scheduler = scheduler
        self._lock = threading.Lock()
        self._listeners: list[Callable[[], None]] = []
        self._pending = False

    def connect(self, listener: Callable[[], None]) -> Cancel:
        """Calls `listener` after every change. The result disconnects it. UI thread only."""
        self._listeners.append(listener)

        def disconnect() -> None:
            if listener in self._listeners:
                self._listeners.remove(listener)

        return disconnect

    def notify(self) -> None:
        with self._lock:
            if self._pending:
                return
            self._pending = True
        self._scheduler.post(self._fire)

    def _fire(self) -> None:
        with self._lock:
            self._pending = False
        for listener in list(self._listeners):
            listener()
