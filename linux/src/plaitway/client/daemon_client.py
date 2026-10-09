"""The connection to plaitwayd: one method per RPC, and the two watches."""

from __future__ import annotations

import logging
import os
from collections.abc import Iterable

import grpc

from . import proto
from .backoff import BackoffPolicy
from .errors import DaemonFailure, FailureKind
from .location import verify_socket_owner
from .types import (
    CredentialKind,
    DaemonInfo,
    Diagnostics,
    LogLine,
    Profile,
    ProfileEvent,
    ProfileImport,
    ProfileSettings,
)
from .watch import LogWatchListener, ProfileWatchListener, ReplayFilter, Watch, run_with_retries

log = logging.getLogger(__name__)

# Calls that return one answer, which the daemon gives within moments; stopping
# an engine is the slowest (the daemon waits at most ten seconds for it).
CALL_TIMEOUT = 30.0
DEFAULT_TAIL_LINES = 200
# sun_path of a Unix socket address, with its terminating NUL.
SOCKET_PATH_LIMIT = 108


class DaemonClient:
    """Blocking calls to the daemon, each raising DaemonFailure, from any thread.

    gRPC connects with the first call, so a daemon that is not running shows
    up there as an UNAVAILABLE failure.
    """

    def __init__(
        self,
        socket_path: str,
        *,
        verify_owner: bool = False,
        trusted_uid: int = 0,
        backoff: BackoffPolicy | None = None,
    ) -> None:
        """
        verify_owner: check, before every call and every restart of a watch,
            that the socket and its directory belong to `trusted_uid` (root) and
            that no one else can write to the directory. For the production
            socket; the development daemon of a user is not root's.
        """
        if len(os.fsencode(socket_path)) >= SOCKET_PATH_LIMIT:
            raise ValueError(f"the socket path is longer than a Unix socket allows: {socket_path}")
        self._socket_path = socket_path
        self._verify_owner = verify_owner
        self._trusted_uid = trusted_uid
        self._backoff = backoff or BackoffPolicy()
        self._methods = proto.methods()
        self._channel = grpc.insecure_channel(
            f"unix:{socket_path}",
            options=[
                # The reconnection of the channel follows the same policy as the watches.
                ("grpc.initial_reconnect_backoff_ms", int(self._backoff.initial * 1000)),
                ("grpc.min_reconnect_backoff_ms", int(self._backoff.initial * 1000)),
                ("grpc.max_reconnect_backoff_ms", int(self._backoff.maximum * 1000)),
            ],
        )
        self._calls = {
            name: self._channel.unary_unary(
                method.path,
                request_serializer=method.request.SerializeToString,
                response_deserializer=method.response.FromString,
            )
            for name, method in self._methods.items()
            if not method.streams
        }
        self._streams = {
            name: self._channel.unary_stream(
                method.path,
                request_serializer=method.request.SerializeToString,
                response_deserializer=method.response.FromString,
            )
            for name, method in self._methods.items()
            if method.streams
        }
        self._watches: set[Watch] = set()

    @property
    def socket_path(self) -> str:
        return self._socket_path

    def close(self) -> None:
        for watch in list(self._watches):
            watch.cancel()
        for watch in list(self._watches):
            watch.join(5)
        self._channel.close()

    # MARK: Calls
    #
    # DaemonFailure kinds: NOT_FOUND for an unknown id, PERMISSION_DENIED for a
    # call that needs an administrator, REJECTED for rejected input and
    # UNAVAILABLE when the daemon is down.

    def _request(self, method: str, **fields):
        return self._methods[method].request(**fields)

    def _call(self, method: str, **fields):
        self._check_server()
        try:
            return self._calls[method](self._request(method, **fields), timeout=CALL_TIMEOUT)
        except grpc.RpcError as error:
            raise DaemonFailure.from_error(error) from error

    def _check_server(self) -> None:
        if self._verify_owner:
            verify_socket_owner(self._socket_path, self._trusted_uid)

    def get_daemon_info(self) -> DaemonInfo:
        return self._call("GetDaemonInfo")

    def get_diagnostics(self) -> Diagnostics:
        return self._call("GetDiagnostics")

    def list_profiles(self) -> list[Profile]:
        return list(self._call("ListProfiles").profiles)

    def set_profile_enabled(self, profile_id: str, enabled: bool) -> Profile:
        """Connects or disconnects a profile. Enabling a failed profile retries it."""
        return self._call("SetProfileEnabled", id=profile_id, enabled=enabled)

    def import_profile(
        self,
        content: bytes,
        source_filename: str,
        name: str = "",
        settings: ProfileSettings | None = None,
    ) -> ProfileImport:
        """Stores a new profile, last in priority order. A rejected profile fails
        with REJECTED and the daemon's reason as the message."""
        fields = {"name": name, "content": content, "source_filename": source_filename}
        if settings is not None:
            fields["settings"] = settings
        return self._import_result(self._call("ImportProfile", **fields))

    def update_profile(
        self, profile_id: str, name: str | None = None, settings: ProfileSettings | None = None
    ) -> Profile:
        """Changes the name, the settings or both."""
        fields: dict = {"id": profile_id}
        if name is not None:
            fields["name"] = name
        if settings is not None:
            fields["settings"] = settings
        return self._call("UpdateProfile", **fields)

    def get_profile_content(self, profile_id: str) -> str:
        """The stored text of a profile, private keys and inline certificates
        included. Needs an administrator."""
        data = self._call("GetProfileContent", id=profile_id).content
        try:
            # Not decoded as "utf-8-sig": a byte order mark the profile starts with is part of it.
            return data.decode("utf-8")
        except UnicodeDecodeError as error:
            raise DaemonFailure(FailureKind.OTHER, "the profile text is not valid UTF-8") from error

    def update_profile_content(self, profile_id: str, content: str, reconnect: bool = False) -> ProfileImport:
        """Replaces the text of a profile with the checks of an import. A
        rejected text fails with REJECTED and the daemon's reason (see
        ConfigDiagnostic), and so does text of the other kind."""
        response = self._call(
            "UpdateProfileContent", id=profile_id, content=content.encode("utf-8"), reconnect=reconnect
        )
        return self._import_result(response)

    def delete_profile(self, profile_id: str) -> None:
        self._call("DeleteProfile", id=profile_id)

    def reorder_profiles(self, ids: Iterable[str]) -> None:
        """`ids` lists every profile, highest priority first."""
        self._call("ReorderProfiles", ids=list(ids))

    def provide_credentials(
        self, profile_id: str, kind: CredentialKind, username: str = "", password: str = ""
    ) -> Profile:
        return self._call(
            "ProvideCredentials", profile_id=profile_id, kind=int(kind), username=username, password=password
        )

    def resync(self) -> None:
        """Re-reads the network and rebuilds every route and DNS entry the daemon owns."""
        self._call("Resync")

    def remove_stale_route(self, key: str) -> None:
        self._call("RemoveStaleRoute", key=key)

    @staticmethod
    def _import_result(response) -> ProfileImport:
        return ProfileImport(profile=response.profile, warnings=list(response.warnings))

    # MARK: Watches

    def watch_profiles(self, listener: ProfileWatchListener) -> Watch:
        """Delivers a snapshot and then one event per change, and again after the
        daemon went away and came back. Runs until cancelled."""

        def attempt(watch: Watch, connected) -> DaemonFailure | None:
            def deliver(event: ProfileEvent, first: bool) -> None:
                if first:
                    connected()
                listener.on_event(event)

            return self._read_stream(watch, "WatchProfiles", {}, deliver)

        return self._start("plaitway-watch-profiles", attempt, listener.on_outage)

    def watch_logs(
        self, profile_id: str, listener: LogWatchListener, tail_lines: int = DEFAULT_TAIL_LINES
    ) -> Watch:
        """The buffered tail of a profile's log and then its live lines, until
        cancelled. An empty `profile_id` is the daemon's own log. After an outage
        the lines that were delivered are not delivered again."""
        replay = ReplayFilter()

        def attempt(watch: Watch, connected) -> DaemonFailure | None:
            replay.restart()

            def deliver(line: LogLine, first: bool) -> None:
                if first:
                    connected()
                if replay.accepts(line):
                    listener.on_line(line)

            failure = self._read_stream(
                watch, "WatchLogs", {"profile_id": profile_id, "tail_lines": tail_lines}, deliver
            )
            if failure is not None and failure.kind is FailureKind.NOT_FOUND:
                watch.cancel()
                listener.on_ended(failure)
                return None
            return failure

        return self._start("plaitway-watch-logs", attempt, listener.on_outage)

    def _start(self, name: str, attempt, outage) -> Watch:
        def run(watch: Watch) -> None:
            try:
                run_with_retries(watch, self._backoff, lambda connected: attempt(watch, connected), outage)
            except Exception:
                log.exception("the %s thread failed", name)
            finally:
                self._watches.discard(watch)

        watch = Watch(name, run)
        self._watches.add(watch)
        return watch.start()

    def _read_stream(self, watch: Watch, method: str, fields: dict, deliver) -> DaemonFailure | None:
        """Reads one stream to its end. Returns why it ended, or None when the
        watch was cancelled. A listener that raises does not end the stream."""
        try:
            self._check_server()
            call = self._streams[method](self._request(method, **fields))
        except DaemonFailure as failure:
            return failure
        watch.attach(call)
        first = True
        try:
            for message in call:
                try:
                    deliver(message, first)
                except Exception:
                    log.exception("a listener of %s failed", method)
                first = False
        except grpc.RpcError as error:
            if watch.stopped:
                return None
            return DaemonFailure.from_error(error)
        if watch.stopped:
            return None
        return DaemonFailure(FailureKind.UNAVAILABLE, "the stream ended")
