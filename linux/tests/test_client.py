"""The connection to the daemon: its location and the trust it needs, its
failures, and the watches through outages. The watches are tested against a
scripted gRPC server, the calls against the real daemon (test_daemon_calls.py)."""

import os
import random
import shutil
import socket
import stat
import tempfile
import threading
import time
import unittest
from concurrent import futures

import grpc

from plaitway.client import proto
from plaitway.client.backoff import BackoffPolicy
from plaitway.client.daemon_client import DaemonClient
from plaitway.client.errors import DaemonFailure, FailureKind
from plaitway.client.location import (
    PRODUCTION_SOCKET_PATH,
    override,
    socket_path,
    verify_socket_owner,
)
from plaitway.client.types import LogLevel, LogLine, Profile, ProfileEvent, ProfileState
from plaitway.client.watch import ReplayFilter


class RpcFailure(grpc.RpcError):
    def __init__(self, code: grpc.StatusCode, details: str) -> None:
        self._code, self._details = code, details

    def code(self):
        return self._code

    def details(self):
        return self._details


class DaemonFailureTests(unittest.TestCase):
    def test_maps_status_codes(self):
        for code, kind in [
            (grpc.StatusCode.PERMISSION_DENIED, FailureKind.PERMISSION_DENIED),
            (grpc.StatusCode.UNAVAILABLE, FailureKind.UNAVAILABLE),
            (grpc.StatusCode.NOT_FOUND, FailureKind.NOT_FOUND),
            (grpc.StatusCode.INVALID_ARGUMENT, FailureKind.REJECTED),
            (grpc.StatusCode.FAILED_PRECONDITION, FailureKind.REJECTED),
            (grpc.StatusCode.INTERNAL, FailureKind.OTHER),
            (grpc.StatusCode.DEADLINE_EXCEEDED, FailureKind.OTHER),
        ]:
            with self.subTest(code=code):
                self.assertEqual(DaemonFailure.from_error(RpcFailure(code, "reason")), DaemonFailure(kind, "reason"))

    def test_wraps_foreign_errors(self):
        self.assertEqual(DaemonFailure.from_error(RuntimeError("boom")).kind, FailureKind.OTHER)

    def test_a_failure_is_itself(self):
        failure = DaemonFailure(FailureKind.SERVER_REFUSED, "x")
        self.assertIs(DaemonFailure.from_error(failure), failure)


class LocationTests(unittest.TestCase):
    def test_uses_the_production_socket_without_an_override(self):
        self.assertEqual(socket_path({}), PRODUCTION_SOCKET_PATH)
        self.assertEqual(socket_path({"PLAITWAY_SOCKET": ""}), PRODUCTION_SOCKET_PATH)
        self.assertIsNone(override({}))

    def test_the_socket_variable_alone_is_not_followed(self):
        # The app answers credential requests from the keyring: a socket named by the
        # environment of a normal run would receive every saved password.
        for environment in [
            {"PLAITWAY_SOCKET": "/tmp/x.sock"},
            {"PLAITWAY_SOCKET": "/tmp/x.sock", "PLAITWAY_DEV": "0"},
            {"PLAITWAY_SOCKET": "/tmp/x.sock", "PLAITWAY_DEV": "true"},
            {"PLAITWAY_DEV": "1"},
        ]:
            with self.subTest(environment=environment):
                self.assertEqual(socket_path(environment), PRODUCTION_SOCKET_PATH)
                self.assertIsNone(override(environment))

    def test_honours_the_socket_variable_with_the_development_switch(self):
        environment = {"PLAITWAY_SOCKET": "/tmp/x.sock", "PLAITWAY_DEV": "1"}
        self.assertEqual(socket_path(environment), "/tmp/x.sock")
        self.assertEqual(override(environment), "/tmp/x.sock")

    def test_the_production_socket_is_rooted_in_run(self):
        self.assertEqual(PRODUCTION_SOCKET_PATH, "/run/plaitway/plaitwayd.sock")


class SocketOwnerTests(unittest.TestCase):
    """The production socket and its directory belong to root; these tests run as
    someone else, so the user they run as plays root."""

    def setUp(self):
        self.directory = tempfile.mkdtemp(prefix="pw-owner-")
        self.addCleanup(shutil.rmtree, self.directory, True)
        self.path = os.path.join(self.directory, "d.sock")
        self.listener = socket.socket(socket.AF_UNIX)
        self.addCleanup(self.listener.close)
        self.listener.bind(self.path)
        self.listener.listen()
        self.uid = os.getuid()

    def assert_refused(self, reason: str, **options) -> None:
        with self.assertRaises(DaemonFailure) as raised:
            verify_socket_owner(self.path, options.pop("trusted_uid", self.uid))
        self.assertEqual(raised.exception.kind, FailureKind.SERVER_REFUSED)
        self.assertIn(reason, raised.exception.message)

    def test_accepts_a_socket_and_a_directory_of_the_trusted_user(self):
        verify_socket_owner(self.path, self.uid)

    def test_a_socket_that_is_writable_by_everyone_is_what_the_helper_makes(self):
        os.chmod(self.path, 0o666)
        verify_socket_owner(self.path, self.uid)

    def test_refuses_a_socket_of_another_user(self):
        self.assert_refused("is owned by user", trusted_uid=self.uid + 1)

    def test_refuses_a_directory_of_another_user(self):
        self.assert_refused("is owned by user", trusted_uid=self.uid + 1)

    def test_refuses_a_directory_others_can_write_to(self):
        for mode in (0o770, 0o707, 0o777):
            with self.subTest(mode=oct(mode)):
                os.chmod(self.directory, mode)
                self.assert_refused("is writable by others")
        os.chmod(self.directory, 0o755)
        verify_socket_owner(self.path, self.uid)

    def test_refuses_a_file_that_is_not_a_socket(self):
        os.unlink(self.path)
        with open(self.path, "w"):
            pass
        self.assert_refused("is not a socket")

    def test_refuses_a_symlink_in_place_of_the_socket(self):
        target = os.path.join(self.directory, "real.sock")
        os.rename(self.path, target)
        os.symlink(target, self.path)
        self.assertTrue(stat.S_ISSOCK(os.stat(self.path).st_mode))
        self.assert_refused("is not a socket")

    def test_refuses_a_symlink_in_place_of_the_directory(self):
        link = tempfile.mkdtemp(prefix="pw-owner-link-")
        self.addCleanup(shutil.rmtree, link, True)
        alias = os.path.join(link, "run")
        os.symlink(self.directory, alias)
        with self.assertRaises(DaemonFailure) as raised:
            verify_socket_owner(os.path.join(alias, "d.sock"), self.uid)
        self.assertIn("is not a directory", raised.exception.message)

    def test_a_socket_that_does_not_exist_is_not_a_refusal(self):
        os.unlink(self.path)
        verify_socket_owner(self.path, self.uid)
        verify_socket_owner(os.path.join(self.directory, "missing", "d.sock"), self.uid)

    def test_the_client_sends_nothing_to_a_socket_it_does_not_trust(self):
        self.listener.setblocking(False)
        client = DaemonClient(self.path, verify_owner=True, trusted_uid=self.uid + 1)
        self.addCleanup(client.close)
        with self.assertRaises(DaemonFailure) as raised:
            client.get_daemon_info()
        self.assertEqual(raised.exception.kind, FailureKind.SERVER_REFUSED)
        time.sleep(0.2)
        with self.assertRaises(BlockingIOError):
            self.listener.accept()

    def test_a_watch_reports_a_server_it_does_not_trust(self):
        client = DaemonClient(self.path, verify_owner=True, trusted_uid=self.uid + 1)
        self.addCleanup(client.close)
        outages = []
        watch = client.watch_profiles(Recorder(outages=outages))
        wait_for(lambda: outages, "the refusal")
        watch.cancel()
        self.assertEqual(outages[0].kind, FailureKind.SERVER_REFUSED)

    def test_refuses_a_socket_path_longer_than_a_unix_socket_allows(self):
        with self.assertRaises(ValueError):
            DaemonClient("/tmp/" + "x" * 120 + ".sock")


class BackoffTests(unittest.TestCase):
    def test_grows_by_the_multiplier_to_the_maximum(self):
        policy = BackoffPolicy(jitter=0)
        delays = [policy.delay(attempt) for attempt in range(12)]
        self.assertAlmostEqual(delays[0], 0.2)
        self.assertAlmostEqual(delays[1], 0.32)
        self.assertAlmostEqual(delays[2], 0.512)
        self.assertEqual(delays[-1], 5.0)
        self.assertEqual(delays, sorted(delays))

    def test_jitters_by_twenty_percent(self):
        policy = BackoffPolicy()
        rng = random.Random(1)
        delays = [policy.delay(30, rng) for _ in range(200)]
        self.assertTrue(all(4.0 <= delay <= 6.0 for delay in delays))
        self.assertGreater(max(delays) - min(delays), 0.5)


# MARK: Watches against a scripted server


def wait_for(condition, what: str, timeout: float = 10.0):
    deadline = time.monotonic() + timeout
    while not (result := condition()):
        if time.monotonic() > deadline:
            raise TimeoutError(f"timed out waiting for {what}")
        time.sleep(0.01)
    return result


class Recorder:
    """A listener that keeps what it is told."""

    def __init__(self, events=None, outages=None, lines=None, ended=None) -> None:
        self.events = [] if events is None else events
        self.outages = [] if outages is None else outages
        self.lines = [] if lines is None else lines
        self.ended = [] if ended is None else ended

    def on_event(self, event):
        self.events.append(event)

    def on_line(self, line):
        self.lines.append(line)

    def on_outage(self, failure):
        self.outages.append(failure)

    def on_ended(self, failure):
        self.ended.append(failure)


def log_line(text: str, seconds: int = 0) -> LogLine:
    line = LogLine(level=LogLevel.INFO, text=text)
    if seconds:
        line.time.seconds = seconds
    return line


class ScriptedServer:
    """A gRPC server that answers each stream from a script and then ends it with
    UNAVAILABLE, as a daemon that goes away does. It listens on a socket of its own
    and can be stopped and started again."""

    def __init__(self) -> None:
        self.directory = tempfile.mkdtemp(prefix="pw-script-")
        self.path = os.path.join(self.directory, "s.sock")
        self.profile_scripts: list[list] = []
        self.log_scripts: list[object] = []
        self.log_requests: list[tuple[str, int]] = []
        self.opened = threading.Event()
        self._server: grpc.Server | None = None

    def start(self) -> None:
        methods = proto.methods()
        server = grpc.server(futures.ThreadPoolExecutor(max_workers=8))
        handlers = {
            "WatchProfiles": grpc.unary_stream_rpc_method_handler(
                self._watch_profiles, methods["WatchProfiles"].request.FromString, methods["WatchProfiles"].response.SerializeToString
            ),
            "WatchLogs": grpc.unary_stream_rpc_method_handler(
                self._watch_logs, methods["WatchLogs"].request.FromString, methods["WatchLogs"].response.SerializeToString
            ),
        }
        server.add_generic_rpc_handlers((grpc.method_handlers_generic_handler("plaitway.v1.DaemonService", handlers),))
        server.add_insecure_port(f"unix:{self.path}")
        server.start()
        self._server = server

    def stop(self) -> None:
        if self._server is not None:
            self._server.stop(0).wait()
            self._server = None
        if os.path.exists(self.path):
            os.unlink(self.path)

    def cleanup(self) -> None:
        self.stop()
        shutil.rmtree(self.directory, ignore_errors=True)

    def _watch_profiles(self, request, context):
        script = self.profile_scripts.pop(0) if self.profile_scripts else None
        if script is None:
            context.abort(grpc.StatusCode.UNAVAILABLE, "no script")
        for event in script:
            if event != "stay":
                yield event
        if script and script[-1] == "stay":
            self.opened.set()
            while context.is_active():
                time.sleep(0.01)
            return
        context.abort(grpc.StatusCode.UNAVAILABLE, "the daemon went away")

    def _watch_logs(self, request, context):
        self.log_requests.append((request.profile_id, request.tail_lines))
        script = self.log_scripts.pop(0) if self.log_scripts else None
        if script == "not found":
            context.abort(grpc.StatusCode.NOT_FOUND, "no such profile")
        if script is None:
            context.abort(grpc.StatusCode.UNAVAILABLE, "no script")
        for line in script:
            yield line
        context.abort(grpc.StatusCode.UNAVAILABLE, "the daemon went away")


def snapshot(*names: str) -> ProfileEvent:
    event = ProfileEvent()
    for name in names:
        profile = event.snapshot.profiles.add()
        profile.id = name
        profile.state = ProfileState.DISCONNECTED
    return event


FAST = BackoffPolicy(initial=0.02, maximum=0.1, multiplier=1.5, jitter=0)


class WatchTests(unittest.TestCase):
    def setUp(self):
        self.server = ScriptedServer()
        self.addCleanup(self.server.cleanup)
        self.server.start()
        self.client = DaemonClient(self.server.path, backoff=FAST)
        self.addCleanup(self.client.close)

    def test_restarts_with_a_snapshot_and_reports_an_outage_once(self):
        self.server.profile_scripts = [[snapshot("a")], [], [], [], [snapshot("a", "b"), "stay"]]
        recorder = Recorder()
        self.client.watch_profiles(recorder)

        wait_for(lambda: len(recorder.events) == 2, "the second snapshot")
        self.assertEqual([len(event.snapshot.profiles) for event in recorder.events], [1, 2])
        # Four attempts failed in a row, and the person is told once; the recovery ends the outage.
        self.assertEqual(len(recorder.outages), 1)
        self.assertEqual(recorder.outages[0].kind, FailureKind.UNAVAILABLE)

    def test_a_second_outage_is_reported_again(self):
        self.server.profile_scripts = [[snapshot("a")], [snapshot("a")], [snapshot("a"), "stay"]]
        recorder = Recorder()
        self.client.watch_profiles(recorder)
        wait_for(lambda: len(recorder.events) == 3, "three snapshots")
        wait_for(lambda: len(recorder.outages) >= 2, "two outages")
        self.assertEqual(len(recorder.outages), 2)

    def test_reports_again_when_the_reason_changes(self):
        # The daemon went away, and then something else listens: refused for another reason.
        reasons = []

        class Reasons(Recorder):
            def on_outage(self, failure):
                reasons.append(failure.kind)

        self.server.stop()
        listener = socket.socket(socket.AF_UNIX)
        self.addCleanup(listener.close)
        client = DaemonClient(self.server.path, verify_owner=True, trusted_uid=os.getuid(), backoff=FAST)
        self.addCleanup(client.close)
        client.watch_profiles(Reasons())
        wait_for(lambda: reasons, "the first outage")
        self.assertEqual(reasons[0], FailureKind.UNAVAILABLE)
        os.chmod(self.server.directory, 0o777)
        listener.bind(self.server.path)
        listener.listen()
        wait_for(lambda: len(reasons) == 2, "the second outage")
        self.assertEqual(reasons, [FailureKind.UNAVAILABLE, FailureKind.SERVER_REFUSED])

    def test_cancelling_ends_the_call_and_the_thread(self):
        self.server.profile_scripts = [[snapshot("a"), "stay"]]
        recorder = Recorder()
        watch = self.client.watch_profiles(recorder)
        wait_for(lambda: self.server.opened.is_set(), "the stream to be open")
        watch.cancel()
        watch.join(5)
        self.assertFalse(watch.is_alive())
        # No outage is reported for a call that was cancelled on purpose.
        self.assertEqual(recorder.outages, [])

    def test_cancelling_during_the_wait_between_attempts_ends_the_thread(self):
        client = DaemonClient(self.server.path, backoff=BackoffPolicy(initial=30, maximum=30, jitter=0))
        self.addCleanup(client.close)
        self.server.stop()
        recorder = Recorder()
        watch = client.watch_profiles(recorder)
        wait_for(lambda: recorder.outages, "the outage")
        watch.cancel()
        watch.join(2)
        self.assertFalse(watch.is_alive())

    def test_closing_the_client_cancels_its_watches(self):
        self.server.profile_scripts = [[snapshot("a"), "stay"]]
        watch = self.client.watch_profiles(Recorder())
        wait_for(lambda: self.server.opened.is_set(), "the stream to be open")
        self.client.close()
        self.assertFalse(watch.is_alive())

    def test_a_listener_that_raises_does_not_end_the_stream(self):
        self.server.profile_scripts = [[snapshot("a"), snapshot("b"), snapshot("c"), "stay"]]
        seen = []

        class Failing(Recorder):
            def on_event(self, event):
                seen.append(event)
                raise RuntimeError("listener bug")

        with self.assertLogs("plaitway.client.daemon_client", "ERROR"):
            self.client.watch_profiles(Failing())
            wait_for(lambda: len(seen) == 3, "all three events")


class LogWatchTests(unittest.TestCase):
    def setUp(self):
        self.server = ScriptedServer()
        self.addCleanup(self.server.cleanup)
        self.server.start()
        self.client = DaemonClient(self.server.path, backoff=FAST)
        self.addCleanup(self.client.close)

    def texts(self, recorder) -> list[str]:
        return [line.text for line in recorder.lines]

    def test_does_not_deliver_again_what_a_restarted_stream_replays(self):
        first = [log_line("one", 1), log_line("two", 2), log_line("three", 3)]
        # The tail is sent again, with a line that came in the meantime.
        replay = [log_line("two", 2), log_line("three", 3), log_line("four", 4)]
        self.server.log_scripts = [first, replay]
        recorder = Recorder()
        self.client.watch_logs("p1", recorder)
        wait_for(lambda: len(recorder.lines) >= 4, "four lines")
        time.sleep(0.2)
        self.assertEqual(self.texts(recorder), ["one", "two", "three", "four"])
        self.assertEqual(self.server.log_requests[0], ("p1", 200))

    def test_delivers_every_line_of_a_stream_that_shares_nothing_with_the_old_one(self):
        # The daemon restarted: its log is a new one, whatever the old one said.
        self.server.log_scripts = [[log_line("old", 1)], [log_line("new", 10), log_line("newer", 11)]]
        recorder = Recorder()
        self.client.watch_logs("", recorder, tail_lines=50)
        wait_for(lambda: len(recorder.lines) >= 3, "three lines")
        self.assertEqual(self.texts(recorder), ["old", "new", "newer"])
        self.assertEqual(self.server.log_requests[0], ("", 50))

    def test_lines_without_a_time_are_counted_not_merged(self):
        self.server.log_scripts = [
            [log_line("same"), log_line("same")],
            [log_line("same"), log_line("same"), log_line("same")],
        ]
        recorder = Recorder()
        self.client.watch_logs("p1", recorder)
        wait_for(lambda: len(recorder.lines) >= 3, "three lines")
        time.sleep(0.2)
        self.assertEqual(self.texts(recorder), ["same", "same", "same"])

    def test_a_profile_that_is_gone_ends_the_watch(self):
        self.server.log_scripts = ["not found"]
        recorder = Recorder()
        watch = self.client.watch_logs("gone", recorder)
        wait_for(lambda: recorder.ended, "the end")
        watch.join(2)
        self.assertFalse(watch.is_alive())
        self.assertEqual(recorder.ended[0].kind, FailureKind.NOT_FOUND)
        self.assertEqual(recorder.outages, [])


class ReplayFilterTests(unittest.TestCase):
    def test_passes_everything_before_the_first_restart(self):
        replay = ReplayFilter()
        self.assertTrue(all(replay.accepts(log_line(text, index + 1)) for index, text in enumerate("abc")))

    def test_ends_the_replay_at_the_first_new_line(self):
        replay = ReplayFilter()
        for index, text in enumerate("abc"):
            replay.accepts(log_line(text, index + 1))
        replay.restart()
        self.assertFalse(replay.accepts(log_line("b", 2)))
        self.assertTrue(replay.accepts(log_line("d", 4)))
        # A line that looks delivered but comes after a new one is new.
        self.assertTrue(replay.accepts(log_line("c", 3)))

    def test_forgets_the_oldest_lines_beyond_its_window(self):
        replay = ReplayFilter(window=3)
        for index in range(10):
            replay.accepts(log_line(f"line {index}", index + 1))
        replay.restart()
        self.assertTrue(replay.accepts(log_line("line 0", 1)))


if __name__ == "__main__":
    unittest.main()
