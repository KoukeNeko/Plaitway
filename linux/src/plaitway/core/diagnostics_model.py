from __future__ import annotations

import threading

from ..client.types import DaemonInfo, Diagnostics
from ..l10n.strings import Strings
from .formatting import format_date_time
from .observable import Notifier
from .ports import UiScheduler
from .presentation import Presenter
from .profile_store import ProfileStore
from .tasks import Ticker
from .user_message import user_message

REFRESH_INTERVAL = 2.0


class DiagnosticsModel:
    """What the Diagnostics page shows, read from the daemon while the page is open."""

    def __init__(self, store: ProfileStore, strings: Strings, scheduler: UiScheduler) -> None:
        self.changed = Notifier(scheduler)
        self._store = store
        self._strings = strings
        self._lock = threading.Lock()
        self._diagnostics: Diagnostics | None = None
        self._failure: str | None = None
        self._ticker: Ticker | None = None

    @property
    def diagnostics(self) -> Diagnostics | None:
        with self._lock:
            return self._diagnostics

    @property
    def failure(self) -> str | None:
        """The latest read failed; the previous reading stays on screen."""
        with self._lock:
            return self._failure

    def start(self) -> None:
        """Reads until `stop()`: routes change with connections and network
        changes, neither of which the profile stream reports."""
        if self._ticker is None:
            self._ticker = Ticker(REFRESH_INTERVAL, self.refresh, "plaitway-diagnostics").start()

    def stop(self) -> None:
        ticker, self._ticker = self._ticker, None
        if ticker is not None:
            ticker.stop()

    def refresh(self) -> None:
        try:
            reading = self._store.fetch_diagnostics()
        except Exception as error:
            with self._lock:
                self._failure = user_message(error, self._strings)
            self.changed.notify()
            return
        with self._lock:
            # Most readings equal the last one; publishing would redraw the page for nothing.
            unchanged = reading == self._diagnostics and self._failure is None
            self._diagnostics = reading
            self._failure = None
        if not unchanged:
            self.changed.notify()


def report_text(
    diagnostics: Diagnostics,
    app_version: str | None,
    daemon: DaemonInfo | None,
    profile_name,
    presenter: Presenter,
) -> str:
    """Everything the Diagnostics page knows, as plain text for a bug report. No
    log lines: they are the person's to review before they share them.
    `profile_name` maps a profile id to its name, or None."""

    def named(owner: str) -> str:
        return profile_name(owner) or owner

    lines = [f"Plaitway {app_version or '-'}"]
    if daemon is not None:
        lines.append(f"Helper {daemon.version} (privileged: {str(daemon.privileged).lower()})")
        for engine in daemon.engines:
            lines.append(f"  {presenter.kind_text(engine.kind)}: {engine.version if engine.available else engine.detail}")

    network = diagnostics.network
    lines += ["", "Network"]
    lines.append(f"  gateway IPv4: {network.default_gateway_v4} {network.default_interface_v4}")
    lines.append(f"  gateway IPv6: {network.default_gateway_v6} {network.default_interface_v6}")
    lines.append(f"  interfaces: {', '.join(network.interfaces)}")
    if network.HasField("last_change"):
        when = format_date_time(network.last_change.seconds + network.last_change.nanos / 1e9)
        lines.append(f"  last change: {when} {network.last_change_reason}")

    lines += ["", "Owned routes"]
    for route in diagnostics.owned_routes:
        lines.append(
            f"  {route.prefix} {presenter.route_state_label(route.state)} "
            f"{presenter.route_kind_label(route.kind)} via {route.via} ({named(route.owner)})"
        )

    lines += ["", "Stale routes"]
    for route in diagnostics.stale_routes:
        owned = " (installed by Plaitway)" if route.owned else ""
        lines.append(f"  {route.prefix} via {route.gateway} {route.interface}: {route.reason}{owned}")

    lines += ["", "Resolver entries"]
    lines += [f"  {entry}" for entry in diagnostics.resolver_entries]

    lines += ["", "Recent changes"]
    for entry in diagnostics.recent_journal[-20:]:
        when = format_date_time(entry.time.seconds + entry.time.nanos / 1e9) if entry.HasField("time") else "-"
        owner = named(entry.owner) if entry.owner else ""
        lines.append(f"  {when} {entry.kind} {entry.key} {entry.state} {owner}")
    return "\n".join(lines)
