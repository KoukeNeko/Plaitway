"""What the tests share: the real daemon on its in-memory backend, profile
fixtures and a wired-up app."""

from __future__ import annotations

import atexit
import logging
import os
import shutil
import signal
import subprocess
import tempfile
import time
import unittest
from pathlib import Path

from plaitway.client.daemon_client import DaemonClient
from plaitway.client.errors import DaemonFailure, FailureKind
from plaitway.core.app_model import AppModel
from plaitway.core.app_state import AppState
from plaitway.core.credentials import InMemoryCredentialStore
from plaitway.core.fakes import FakeScheduler, fake_platform
from plaitway.core.profile_store import Connection, ProfileStore
from plaitway.core.tasks import TaskRunner
from plaitway.l10n.strings import Strings

# The models log what they could not do; a test that expects it says so with assertLogs.
logging.getLogger("plaitway").addHandler(logging.NullHandler())

REPO_ROOT = Path(__file__).resolve().parents[2]

_built: dict[str, str] = {}


def daemon_binary() -> str:
    """The daemon the tests run: PLAITWAY_DAEMON, else a build of cmd/plaitwayd."""
    if "path" in _built:
        return _built["path"]
    path = os.environ.get("PLAITWAY_DAEMON")
    if not path:
        if shutil.which("go") is None:
            raise unittest.SkipTest("PLAITWAY_DAEMON is not set and there is no go to build the daemon with")
        directory = tempfile.mkdtemp(prefix="pw-bin-")
        atexit.register(shutil.rmtree, directory, True)
        path = os.path.join(directory, "plaitwayd")
        build = subprocess.run(
            ["go", "build", "-o", path, "./cmd/plaitwayd"], cwd=REPO_ROOT, capture_output=True, text=True
        )
        if build.returncode != 0:
            raise RuntimeError(f"go build ./cmd/plaitwayd failed:\n{build.stderr}")
    _built["path"] = path
    return path


def repository_text(relative_path: str) -> str:
    """A text file of the repository, such as a profile in internal/ovpn/testdata."""
    # Bytes: reading as text would turn CR LF into LF.
    return (REPO_ROOT / relative_path).read_bytes().decode("utf-8")


class DaemonProcess:
    """Runs the real Go daemon on its in-memory backend (`-fake`), with its own
    socket and an empty state directory. Without `fake` it runs the real
    engines, which without root can parse and store profiles but not connect
    them: for what only the real profile parser decides."""

    def __init__(self, fake: bool = True) -> None:
        self.fake = fake
        # The socket path must stay short: sun_path allows 107 bytes.
        self.directory = tempfile.mkdtemp(prefix="pw-")
        self.socket_path = os.path.join(self.directory, "d.sock")
        self._process: subprocess.Popen | None = None

    def start(self) -> None:
        run_directory = os.path.join(self.directory, "run")
        command = [daemon_binary()] + (["-fake"] if self.fake else [])
        command += ["-socket", self.socket_path, "-state-dir", os.path.join(self.directory, "state"), "-run-dir", run_directory]
        self.log_path = os.path.join(self.directory, "daemon.log")
        with open(self.log_path, "ab") as log:
            self._process = subprocess.Popen(command, stderr=log)
        deadline = time.monotonic() + 5
        while not os.path.exists(self.socket_path):
            if self._process.poll() is not None:
                reason = f"the daemon exited with status {self._process.returncode}: {self._last_log_line()}"
                if not self.fake:
                    raise unittest.SkipTest(f"the daemon cannot run its real engines here ({reason})")
                raise RuntimeError(reason)
            if time.monotonic() > deadline:
                raise RuntimeError(f"the daemon did not create {self.socket_path}")
            time.sleep(0.02)
        self._probe()

    def _last_log_line(self) -> str:
        try:
            with open(self.log_path, encoding="utf-8", errors="replace") as log:
                return (log.read().strip().splitlines() or [""])[-1]
        except OSError:
            return ""

    def _probe(self) -> None:
        client = DaemonClient(self.socket_path)
        try:
            # A daemon started again on the same socket path is reached only when the
            # channel's reconnect backoff (up to five seconds) is over, and the
            # older gRPC of the distributions waits it out where the newest does not.
            for _ in range(400):
                try:
                    client.get_daemon_info()
                    return
                except DaemonFailure as failure:
                    if failure.kind is FailureKind.PERMISSION_DENIED:
                        raise unittest.SkipTest(f"the daemon refuses this user: {failure.message}") from failure
                    time.sleep(0.05)
            raise RuntimeError("the daemon does not answer")
        finally:
            client.close()

    def is_running(self) -> bool:
        return self._process is not None and self._process.poll() is None

    def kill(self) -> None:
        """SIGKILL, like a crash: the socket file is left behind."""
        if self._process is not None and self._process.poll() is None:
            self._process.send_signal(signal.SIGKILL)
            self._process.wait()

    def cleanup(self) -> None:
        self.kill()
        shutil.rmtree(self.directory, ignore_errors=True)


class DaemonTestCase(unittest.TestCase):
    """One daemon for the class; every test starts with no profiles."""

    fake = True
    daemon: DaemonProcess

    @classmethod
    def setUpClass(cls) -> None:
        super().setUpClass()
        cls.daemon = DaemonProcess(fake=cls.fake)
        cls.addClassCleanup(cls.daemon.cleanup)
        cls.daemon.start()

    def ensure_daemon_running(self) -> None:
        """A test that stopped the daemon leaves it running for the next one."""
        if not self.daemon.is_running():
            self.daemon.start()

    def setUp(self) -> None:
        super().setUp()
        self.ensure_daemon_running()
        self.client = DaemonClient(self.daemon.socket_path)
        self.addCleanup(self.client.close)
        for profile in self.client.list_profiles():
            self.client.delete_profile(profile.id)


def openvpn_profile(host: str = "vpn.example.net", markers: list[str] = (), extra: list[str] = ()) -> bytes:
    """A profile in the style of the router profile the product has to handle.
    The fake daemon reads `markers` (`# fake: ...`) to decide how it behaves."""
    lines = [
        "client", "dev tun", "proto tcp-client", f"remote {host} 1194", "route 192.168.1.0 255.255.255.0",
        *extra, *markers,
        "<ca>", "-----BEGIN CERTIFICATE-----", "MIIBtestonlynotacertificate", "-----END CERTIFICATE-----", "</ca>",
    ]
    return ("\n".join(lines) + "\n").encode()


def wireguard_profile(allowed_ips: str = "0.0.0.0/0", endpoint: str = "203.0.113.5:51820") -> bytes:
    return (
        "[Interface]\n"
        "PrivateKey = AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n"
        "Address = 10.6.0.2/32\n"
        "DNS = 10.6.0.1\n"
        "\n"
        "[Peer]\n"
        "PublicKey = BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=\n"
        f"AllowedIPs = {allowed_ips}\n"
        f"Endpoint = {endpoint}\n"
    ).encode()


NEEDS_CREDENTIALS = openvpn_profile(markers=["# fake: needs-credentials"])


class App:
    """The models wired to a daemon, with the ports faked and the UI thread the test's."""

    def __init__(
        self,
        socket_path: str,
        credentials: InMemoryCredentialStore | None = None,
        *,
        language: str = "en",
        app_version: str | None = None,
        is_overridden: bool = True,
    ) -> None:
        self.scheduler = FakeScheduler()
        self.platform = fake_platform(self.scheduler)
        self.runner = TaskRunner(self.scheduler)
        self.credentials = credentials or InMemoryCredentialStore()
        self.strings = Strings(language)
        self.client = DaemonClient(socket_path)
        self.store = ProfileStore(self.client, self.credentials, self.scheduler, self.runner)
        state_directory = tempfile.mkdtemp(prefix="pw-state-")
        self._state_directory = state_directory
        self.model = AppModel(
            self.store,
            self.platform,
            self.runner,
            self.strings,
            AppState(Path(state_directory) / "state.json"),
            app_version=app_version,
            is_overridden=is_overridden,
        )

    @property
    def dialogs(self):
        return self.platform.dialogs

    def start(self) -> App:
        self.model.start()
        self.wait_until(lambda: self.store.connection is Connection.CONNECTED, "the first snapshot")
        return self

    def wait_until(self, condition, what: str = "the condition", timeout: float = 10.0):
        return self.scheduler.wait_until(condition, what, timeout)

    def close(self) -> None:
        self.model.stop()
        self.client.close()
        self.runner.shutdown()
        shutil.rmtree(self._state_directory, ignore_errors=True)


class AppTestCase(DaemonTestCase):
    """A test with a daemon and an app wired to it."""

    def make_app(self, credentials: InMemoryCredentialStore | None = None, **options) -> App:
        app = App(self.daemon.socket_path, credentials, **options)
        self.addCleanup(app.close)
        return app.start()

    def import_fixture(self, app: App, filename: str, content: bytes = b"", settings=None) -> str:
        """Imports a profile and waits until the store shows it."""
        result = app.store.import_profile(content or openvpn_profile(), filename, settings=settings)
        profile_id = result.profile.id
        app.wait_until(lambda: app.store.profile(profile_id) is not None, f"{filename} to appear")
        return profile_id

    def wait_for_state(self, app: App, profile_id: str, state) -> None:
        app.wait_until(
            lambda: (p := app.store.profile(profile_id)) is not None and p.state == state,
            f"{profile_id} to be {state!r}",
        )
