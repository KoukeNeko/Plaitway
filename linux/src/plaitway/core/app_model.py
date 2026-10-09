"""The app's state beyond the profiles: which page is open, what is being asked
or reported, and the helper. Every command goes to the daemon and comes back
through the store.

State changes from any thread and is read from any thread; `changed` tells the
UI thread to read it again. Commands do not block: they work on a thread of
their own and report what they could not do in an alert.
"""

from __future__ import annotations

import enum
import logging
import os
import threading
import time
from collections.abc import Callable, Iterable
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass

from ..client import profile_importer
from ..client.errors import DaemonFailure, FailureKind
from ..client.profile_set import moved, moved_by
from ..client.types import Diagnostics, Profile, ProfileSettings, StaleRoute
from ..l10n.strings import Strings
from .app_state import AppState
from .diagnostics_model import report_text
from .import_report import ImportOutcome, ImportReport
from .log_tail import LogTail
from .observable import Notifier
from .ports import (
    Cancel,
    Confirmation,
    HelperRegistration,
    Platform,
    QuitAnswer,
)
from .presentation import Presenter
from .profile_editor import ProfileEditor
from .profile_store import Connection, ProfileStore
from .setup import DaemonSetup, SetupAction, SetupContent, SetupState, resolve_setup, setup_content
from .tasks import Outcome, TaskRunner, Ticker
from .traffic_history import TrafficHistory
from .user_message import user_message

log = logging.getLogger(__name__)

# How long a helper that was just started or retried is given before it counts as not answering.
SETTLE_DURATION = 8.0
# How often the app looks at the helper's unit while it is not connected to it, and at
# its byte counters: the daemon reports a profile only when something about it changes,
# so a tunnel that went quiet would keep its last rate and draw a line across the silence.
POLL_INTERVAL = 2.0
# While connected the unit only matters for a banner and the Settings page.
CONNECTED_HELPER_INTERVAL = 30.0
# How long a profile that was just imported is waited for in the store.
SELECT_DEADLINE = 2.0


class SelectionKind(enum.Enum):
    PROFILE = "profile"
    DIAGNOSTICS = "diagnostics"
    SETTINGS = "settings"


@dataclass(frozen=True)
class Selection:
    """What the sidebar has selected."""

    kind: SelectionKind
    profile_id: str | None = None

    @staticmethod
    def profile(profile_id: str) -> Selection:
        return Selection(SelectionKind.PROFILE, profile_id)


DIAGNOSTICS = Selection(SelectionKind.DIAGNOSTICS)
SETTINGS = Selection(SelectionKind.SETTINGS)


class ProfileSection(enum.Enum):
    OVERVIEW = 0
    ROUTES = 1
    LOGS = 2
    CONFIGURATION = 3
    SETTINGS = 4

    def label(self, strings: Strings) -> str:
        match self:
            case ProfileSection.OVERVIEW:
                return strings.overview
            case ProfileSection.ROUTES:
                return strings.routes_and_dns
            case ProfileSection.LOGS:
                return strings.logs
            case ProfileSection.CONFIGURATION:
                return strings.configuration
            case ProfileSection.SETTINGS:
                return strings.settings


class DiagnosticsPage(enum.Enum):
    OVERVIEW = 0
    HELPER_LOG = 1

    def label(self, strings: Strings) -> str:
        return strings.overview if self is DiagnosticsPage.OVERVIEW else strings.helper_log


class AlertTitle(enum.Enum):
    IMPORT_FAILED = "import failed"
    DELETE_FAILED = "delete failed"
    REORDER_FAILED = "reorder failed"
    SAVE_FAILED = "save failed"
    CONNECT_FAILED = "connect failed"
    HELPER_FAILED = "helper failed"
    LOGIN_ITEM_FAILED = "launch at login failed"
    RESYNC_FAILED = "resync failed"
    REMOVE_FAILED = "remove failed"
    CREDENTIALS_NOT_SAVED = "credentials not saved"

    def label(self, strings: Strings) -> str:
        match self:
            case AlertTitle.IMPORT_FAILED:
                return strings.import_failed
            case AlertTitle.DELETE_FAILED:
                return strings.delete_failed
            case AlertTitle.REORDER_FAILED:
                return strings.reorder_failed
            case AlertTitle.SAVE_FAILED:
                return strings.save_failed
            case AlertTitle.CONNECT_FAILED:
                return strings.connect_failed
            case AlertTitle.HELPER_FAILED:
                return strings.helper_failed
            case AlertTitle.LOGIN_ITEM_FAILED:
                return strings.launch_at_login_failed
            case AlertTitle.RESYNC_FAILED:
                return strings.resync_failed
            case AlertTitle.REMOVE_FAILED:
                return strings.remove_failed
            case AlertTitle.CREDENTIALS_NOT_SAVED:
                return strings.credentials_not_saved


class AppModel:
    def __init__(
        self,
        store: ProfileStore,
        platform: Platform,
        runner: TaskRunner,
        strings: Strings,
        state: AppState,
        *,
        app_version: str | None,
        is_overridden: bool,
    ) -> None:
        self.store = store
        self.platform = platform
        self.strings = strings
        self.presenter = Presenter(strings)
        self.app_state = state
        self.app_version = app_version
        self.is_overridden = is_overridden
        self.traffic = TrafficHistory()
        self.changed = Notifier(platform.scheduler)
        #: The byte counters were read again; the graphs of the Overview page redraw.
        self.traffic_changed = Notifier(platform.scheduler)
        self._runner = runner
        self._lock = threading.RLock()
        self._selection: Selection | None = None
        self._profile_section = ProfileSection.OVERVIEW
        self._diagnostics_page = DiagnosticsPage.OVERVIEW
        self._search_request = 0
        self._compact = False
        self._registration: HelperRegistration | None = None
        self._helper_asked = 0.0
        self._is_settling = False
        self._settle_cancel: Cancel | None = None
        self._pending_selection: str | None = None
        self._pending_cancel: Cancel | None = None
        self._editors: dict[str, ProfileEditor] = {}
        self._presented_prompts: set[str] = set()
        self._tickers: list[Ticker] = []
        self._disconnect_store_changed: Cancel | None = None

    # MARK: Life

    def start(self) -> None:
        """Follows the daemon. Call on the UI thread, once."""
        self._disconnect_store_changed = self.store.changed.connect(self._store_changed)
        self.store.on_credentials_not_saved = self.report_credentials_not_saved
        self.store.start()
        self._tickers = [
            Ticker(POLL_INTERVAL, self._sample_traffic, "plaitway-traffic").start(),
            Ticker(POLL_INTERVAL, self._poll_helper, "plaitway-helper").start(),
        ]

    def stop(self) -> None:
        for ticker in self._tickers:
            ticker.stop()
        self._tickers = []
        if self._disconnect_store_changed is not None:
            self._disconnect_store_changed()
        for editor in self._editors.values():
            editor.hide_secrets()
        self.store.stop()

    def _store_changed(self) -> None:
        """On the UI thread, after the store changed."""
        self.traffic.record(self.store.profiles)
        self.traffic_changed.notify()
        self.reconcile_selection()
        self._sync_credential_dialogs()
        self.changed.notify()

    # MARK: State

    @property
    def selection(self) -> Selection | None:
        with self._lock:
            return self._selection

    def select(self, selection: Selection | None) -> None:
        with self._lock:
            self._selection = selection
        self.changed.notify()

    @property
    def profile_section(self) -> ProfileSection:
        """The page of a profile that is open; it stays when another profile is selected."""
        with self._lock:
            return self._profile_section

    def show_section(self, section: ProfileSection) -> None:
        """Opens a page of the selected profile, or of the first one when none is."""
        with self._lock:
            self._profile_section = section
            if self.selected_profile is None and (first := next(iter(self.store.profiles), None)):
                self._selection = Selection.profile(first.id)
        self.changed.notify()

    @property
    def diagnostics_page(self) -> DiagnosticsPage:
        with self._lock:
            return self._diagnostics_page

    def show_diagnostics_page(self, page: DiagnosticsPage) -> None:
        with self._lock:
            self._diagnostics_page = page
        self.changed.notify()

    @property
    def compact(self) -> bool:
        """The window shows one pane at a time: pages with more than fits show less."""
        with self._lock:
            return self._compact

    def set_compact(self, compact: bool) -> None:
        with self._lock:
            if compact == self._compact:
                return
            self._compact = compact
        self.changed.notify()

    @property
    def search_request(self) -> int:
        """Counts Ctrl+F: the log on screen moves the focus to its search field."""
        with self._lock:
            return self._search_request

    def request_search(self) -> None:
        with self._lock:
            self._search_request += 1
        self.changed.notify()

    @property
    def selected_profile(self) -> Profile | None:
        selection = self.selection
        if selection is None or selection.kind is not SelectionKind.PROFILE:
            return None
        return self.store.profile(selection.profile_id)

    @property
    def has_pages(self) -> bool:
        """Whether what is selected has pages to switch between: a profile and
        Diagnostics have, Settings has not."""
        selection = self.selection
        return self.selected_profile is not None or (selection is not None and selection.kind is SelectionKind.DIAGNOSTICS)

    def profile_name(self, profile_id: str) -> str | None:
        profile = self.store.profile(profile_id)
        return profile.name if profile else None

    @property
    def registration(self) -> HelperRegistration | None:
        with self._lock:
            return self._registration

    @property
    def is_settling(self) -> bool:
        with self._lock:
            return self._is_settling

    @property
    def setup(self) -> DaemonSetup:
        failure = self.store.failure
        info = self.store.daemon_info
        return resolve_setup(
            connection=self.store.connection,
            registration=self.registration,
            failure_kind=failure.kind if failure else None,
            failure_message=failure.message if failure else "",
            daemon_version=info.version if info else None,
            app_version=self.app_version,
            is_overridden=self.is_overridden,
            is_settling=self.is_settling,
        )

    @property
    def setup_content(self) -> SetupContent | None:
        """What replaces the window while the helper is not ready; None while the profiles can be shown."""
        if self.keeps_profiles_in_view:
            return None
        return setup_content(self.setup, self.strings)

    @property
    def keeps_profiles_in_view(self) -> bool:
        """The helper stopped answering after it had shown its profiles: they stay
        in the window (with a note) instead of giving way to a setup page, which a
        restart would flash."""
        return self.setup.state in (SetupState.NOT_RESPONDING, SetupState.CONNECTING) and bool(self.store.profiles)

    @property
    def can_manage_helper(self) -> bool:
        """The helper is a systemd unit this app may start and restart."""
        return not self.is_overridden and self.registration in (HelperRegistration.RUNNING, HelperRegistration.STOPPED)

    @property
    def switched_on_profiles(self) -> list[Profile]:
        """The profiles the helper keeps switched on, whether or not the app runs; none
        while the helper does not answer, because what the profiles show is then stale."""
        if not self.setup.is_usable:
            return []
        return [profile for profile in self.store.profiles if profile.desired_enabled]

    @property
    def needs_window(self) -> bool:
        """A profile asks for credentials: the dialog needs a window."""
        return bool(self.store.credential_prompts)

    @property
    def show_tray_icon(self) -> bool:
        return self.app_state.show_tray_icon

    def set_show_tray_icon(self, shown: bool) -> None:
        self.app_state.set_show_tray_icon(shown)
        self.changed.notify()

    def editor(self, profile: Profile) -> ProfileEditor:
        """The editor of a profile's text; made when first asked for and kept until the profile is gone."""
        with self._lock:
            if profile.id not in self._editors:
                self._editors[profile.id] = ProfileEditor(
                    profile.id, profile.kind, self.store, self.strings, self.platform.scheduler, self._runner
                )
            return self._editors[profile.id]

    @property
    def has_unsaved_edits(self) -> bool:
        """Profile text was changed in an editor and not saved."""
        with self._lock:
            return any(editor.is_dirty for editor in self._editors.values())

    # MARK: Helper

    def _poll_helper(self) -> None:
        now = time.monotonic()
        if (
            self.store.connection is Connection.CONNECTED
            and self.registration is not None
            and now - self._helper_asked < CONNECTED_HELPER_INTERVAL
        ):
            return
        self._helper_asked = now
        self.refresh_helper()

    def refresh_helper(self) -> None:
        """Asks systemd about the helper; blocks."""
        before = self.registration
        try:
            registration = self.platform.helper.registration()
        except Exception:
            log.exception("could not ask systemd about the helper")
            registration = HelperRegistration.UNKNOWN
        with self._lock:
            self._registration = registration
        if before is not HelperRegistration.RUNNING and registration is HelperRegistration.RUNNING:
            self._begin_settling()
        if registration != before:
            self.changed.notify()

    def start_helper(self) -> None:
        self._run(self.platform.helper.start, AlertTitle.HELPER_FAILED, done=self._helper_changed)

    def restart_helper(self) -> None:
        self._run(self.platform.helper.restart, AlertTitle.HELPER_FAILED, done=self._helper_changed)

    def request_restart_helper(self) -> None:
        """Restarting drops every tunnel, so every way to it asks first."""
        s = self.strings
        self.platform.dialogs.confirm(
            Confirmation(s.restart_helper_title, s.connected_profiles_disconnect, s.restart),
            lambda confirmed: self.restart_helper() if confirmed else None,
        )

    def _helper_changed(self, outcome: Outcome) -> None:
        if outcome.ok:
            self._begin_settling()
        self._run(self.refresh_helper)

    def retry(self) -> None:
        """Looks at the helper again; the daemon may have come up since."""
        self._begin_settling()
        self._run(self.refresh_helper)

    def perform(self, action: SetupAction) -> None:
        match action:
            case SetupAction.START_HELPER:
                self.start_helper()
            case SetupAction.RESTART_HELPER:
                self.request_restart_helper()
            case SetupAction.RETRY:
                self.retry()

    def _begin_settling(self) -> None:
        with self._lock:
            self._is_settling = True
            if self._settle_cancel is not None:
                self._settle_cancel()
            self._settle_cancel = self.platform.scheduler.call_later(SETTLE_DURATION, self._end_settling)
        self.changed.notify()

    def _end_settling(self) -> None:
        with self._lock:
            self._is_settling = False
        self.changed.notify()

    @property
    def launches_at_login(self) -> bool:
        return self.platform.login_item.is_enabled

    def set_launch_at_login(self, enabled: bool) -> None:
        self._run(lambda: self.platform.login_item.set_enabled(enabled), AlertTitle.LOGIN_ITEM_FAILED,
                  done=lambda _: self.changed.notify())

    # MARK: Profiles

    def set_enabled(self, enabled: bool, profile_id: str) -> None:
        self._run(lambda: self.store.set_enabled(enabled, profile_id), AlertTitle.CONNECT_FAILED)

    def toggle(self, profile: Profile) -> None:
        """Connect when it is off, disconnect when it is on."""
        self.set_enabled(not profile.desired_enabled, profile.id)

    def disconnect_all(self, done: Callable[[bool], None] | None = None) -> None:
        """Switches off every profile that is on. `done` gets whether all of them are
        off; a profile that stays on is reported."""
        ids = [profile.id for profile in self.switched_on_profiles]

        def stop(profile_id: str) -> Exception | None:
            try:
                self.store.set_enabled(False, profile_id)
            except Exception as error:
                return error
            return None

        def job() -> list[Exception]:
            if not ids:
                return []
            # Together: each stop can take seconds, and the app waits for the last of them to quit.
            with ThreadPoolExecutor(max_workers=len(ids)) as pool:
                return [error for error in pool.map(stop, ids) if error is not None]

        def finished(outcome: Outcome) -> None:
            for error in outcome.value or []:
                self.report(error, AlertTitle.CONNECT_FAILED)
            if done is not None:
                done(outcome.ok and not outcome.value)

        self._runner.run(job, finished)

    def choose_import(self) -> None:
        """Asks for profile files and imports them."""
        if self.setup.is_usable:
            self.platform.file_picker.choose_profiles(self.import_files)

    def import_files(self, paths: Iterable[str]) -> None:
        """Imports the files in order and selects the last profile that was stored."""
        paths = list(paths)

        def job() -> tuple[ImportReport, str | None]:
            outcomes: list[ImportOutcome] = []
            last_imported: str | None = None
            for path in paths:
                filename = os.path.basename(path)
                try:
                    loaded = profile_importer.load(path)
                    result = self.store.import_profile(loaded.content, filename, credentials=loaded.credentials)
                    last_imported = result.profile.id
                    outcomes.append(ImportOutcome(filename, result.profile.name, tuple(result.warnings)))
                except Exception as error:  # reported with the file it belongs to
                    log.error("import of %s failed: %s", filename, error)
                    outcomes.append(ImportOutcome(filename, failure=user_message(error, self.strings)))
            return ImportReport(tuple(outcomes)), last_imported

        def finished(outcome: Outcome) -> None:
            report, last_imported = outcome.value
            if report.needs_attention:
                self.platform.dialogs.show_import_report(report)
            if last_imported:
                self._select_when_shown(last_imported)

        self._runner.run(job, finished)

    def request_deletion(self, profile_id: str) -> None:
        profile = self.store.profile(profile_id)
        if profile is None:
            return
        s = self.strings
        self.platform.dialogs.confirm(
            Confirmation(s.delete_profile_title(profile.name), s.delete_profile_message, s.delete),
            lambda confirmed: self.delete(profile_id) if confirmed else None,
        )

    def delete(self, profile_id: str) -> None:
        self._run(lambda: self.store.delete_profile(profile_id), AlertTitle.DELETE_FAILED)

    def move_to(self, ids: list[str]) -> None:
        """Sets the priority order: every profile, highest priority first."""
        self._run(lambda: self.store.reorder(ids), AlertTitle.REORDER_FAILED)

    def move(self, source: Iterable[int], destination: int) -> None:
        """Moves the profiles at the positions `source` to just before `destination`, like a drag."""
        self.move_to(moved(self.store.profiles.ids, source, destination))

    def move_by(self, profile_id: str, delta: int) -> None:
        """`delta` -1 moves the profile up one place, +1 down."""
        ids = moved_by(self.store.profiles.ids, profile_id, delta)
        if ids is not None:
            self.move_to(ids)

    def rename(self, profile_id: str, name: str, done: Callable[[bool], None] | None = None) -> None:
        """`done` gets whether the profile has the name, or is about to; when it does
        not, the text the person typed is no longer worth showing."""
        name = name.strip()
        profile = self.store.profile(profile_id)
        if profile is None or not name:
            if done:
                done(False)
            return
        if name == profile.name:
            if done:
                done(True)
            return

        def finished(outcome: Outcome) -> None:
            if not outcome.ok:
                self.report(outcome.error, AlertTitle.SAVE_FAILED)
            if done:
                done(outcome.ok)

        self._runner.run(lambda: self.store.update_profile(profile_id, name=name), finished)

    def change_settings(self, profile_id: str, change: Callable[[ProfileSettings], None]) -> None:
        """Changes one setting; the others stay as the daemon reports them."""
        profile = self.store.profile(profile_id)
        if profile is None:
            return
        settings = ProfileSettings()
        settings.CopyFrom(profile.settings)
        change(settings)
        self._run(lambda: self.store.update_profile(profile_id, settings=settings), AlertTitle.SAVE_FAILED)

    def check_saved_credentials(self, profile_id: str, done: Callable[[bool], None]) -> None:
        """`done` gets whether the credential store holds an answer for the profile."""
        self._runner.run(
            lambda: self.store.has_saved_credentials(profile_id), lambda outcome: done(bool(outcome.ok and outcome.value))
        )

    def forget_saved_credentials(self, profile_id: str, done: Callable[[], None] | None = None) -> None:
        self._runner.run(lambda: self.store.forget_saved_credentials(profile_id), lambda _: done() if done else None)

    # MARK: Diagnostics

    def resync(self, done: Callable[[], None] | None = None) -> None:
        self._run(self.store.resync, AlertTitle.RESYNC_FAILED, done=lambda _: done() if done else None)

    def request_remove_stale_route(self, route: StaleRoute, done: Callable[[], None] | None = None) -> None:
        s = self.strings
        self.platform.dialogs.confirm(
            Confirmation(s.remove_route_title(route.prefix), "", s.remove),
            lambda confirmed: self.remove_stale_route(route.key, done) if confirmed else None,
        )

    def remove_stale_route(self, key: str, done: Callable[[], None] | None = None) -> None:
        def finished(outcome: Outcome) -> None:
            if not outcome.ok:
                if DaemonFailure.from_error(outcome.error).kind is FailureKind.NOT_FOUND:
                    # Already gone: a refresh or a resync removed it while the dialog was open.
                    log.info("the stale route %s was already gone", key)
                else:
                    self.report(outcome.error, AlertTitle.REMOVE_FAILED)
            if done:
                done()

        self._runner.run(lambda: self.store.remove_stale_route(key), finished)

    def copy_report(self, diagnostics: Diagnostics) -> None:
        self.platform.clipboard.set_text(
            report_text(diagnostics, self.app_version, self.store.daemon_info, self.profile_name, self.presenter)
        )

    def run_job(self, job: Callable[[], object], done: Callable[[Outcome], None] | None = None) -> None:
        """Runs a job that blocks off the UI thread; `done` is called on it."""
        self._runner.run(job, done)

    def copy(self, text: str) -> None:
        self.platform.clipboard.set_text(text)

    def log_tail(self, profile_id: str) -> LogTail:
        """A tail of a profile's log, or of the daemon's own for an empty id; not started."""
        return LogTail(self.store.client, profile_id, self.platform.scheduler)

    # MARK: Credentials

    def submit_credentials(self, profile_id: str, username: str, password: str) -> None:
        def finished(outcome: Outcome) -> None:
            if not outcome.ok:
                # The daemon did not take them: the profile still asks, and the dialog that was answered is gone.
                self._presented_prompts.discard(profile_id)
                self._sync_credential_dialogs()

        self._run(
            lambda: self.store.provide_credentials(profile_id, username, password), AlertTitle.CONNECT_FAILED, done=finished
        )

    def cancel_credentials(self, profile_id: str) -> None:
        self._run(lambda: self.store.cancel_credentials(profile_id), AlertTitle.CONNECT_FAILED)

    def report_credentials_not_saved(self, error: Exception) -> None:
        self.report(error, AlertTitle.CREDENTIALS_NOT_SAVED)

    def _sync_credential_dialogs(self) -> None:
        """Asks for what the daemon asks for, and stops asking when it does not."""
        prompts = {prompt.profile_id: prompt for prompt in self.store.credential_prompts}
        for profile_id in list(self._presented_prompts - prompts.keys()):
            self._presented_prompts.discard(profile_id)
            self.platform.dialogs.dismiss_credentials(profile_id)
        for profile_id, prompt in prompts.items():
            if profile_id in self._presented_prompts:
                continue
            self._presented_prompts.add(profile_id)
            self.platform.dialogs.ask_credentials(
                prompt,
                self.profile_name(profile_id) or profile_id,
                lambda username, password, pid=profile_id: self.submit_credentials(pid, username, password),
                lambda pid=profile_id: self.cancel_credentials(pid),
            )

    # MARK: Selection

    def reconcile_selection(self) -> None:
        """Keeps something selected while there is something to select: after a
        deletion the first profile, and the first profile at launch."""
        profiles = self.store.profiles
        existing = set(profiles.ids)
        with self._lock:
            self._editors = {key: editor for key, editor in self._editors.items() if key in existing}
            if self._pending_selection in existing:
                self._selection = Selection.profile(self._pending_selection)
                self._pending_selection = None
                return
            selection = self._selection
            if selection is not None and selection.kind is not SelectionKind.PROFILE:
                return
            if selection is not None and selection.profile_id in existing:
                return
            self._selection = Selection.profile(profiles[0].id) if profiles else None

    def _select_when_shown(self, profile_id: str) -> None:
        """Selects a profile once the store shows it; the daemon's event may arrive after the reply."""
        with self._lock:
            self._pending_selection = profile_id
            if self._pending_cancel is not None:
                self._pending_cancel()
            self._pending_cancel = self.platform.scheduler.call_later(SELECT_DEADLINE, self._drop_pending_selection)
        self.reconcile_selection()
        self.changed.notify()

    def _drop_pending_selection(self) -> None:
        with self._lock:
            self._pending_selection = None

    # MARK: Quitting

    def request_quit(self, proceed: Callable[[], None], blocked: Callable[[], None] | None = None) -> None:
        """Asks what Quit needs asked, then calls `proceed`; `blocked` when the person
        or a profile that stays on stops it. The helper, not the app, keeps profiles
        connected, which is not what everyone expects of Quit."""

        def stop() -> None:
            if blocked:
                blocked()

        def ask_about_profiles() -> None:
            if not self.switched_on_profiles:
                proceed()
                return

            def answered(answer: QuitAnswer) -> None:
                match answer:
                    case QuitAnswer.QUIT:
                        proceed()
                    case QuitAnswer.CANCEL:
                        stop()
                    case QuitAnswer.DISCONNECT_AND_QUIT:
                        # A profile that stays on is reported in the window.
                        self.disconnect_all(lambda all_off: proceed() if all_off else stop())

            self.platform.quit_prompts.ask_quit(answered)

        # Profile text that was changed and not saved is lost with the app.
        if self.has_unsaved_edits:
            self.platform.quit_prompts.confirm_discarding_edits(
                lambda quit_anyway: ask_about_profiles() if quit_anyway else stop()
            )
        else:
            ask_about_profiles()

    # MARK: Failures

    def report(self, error: BaseException, title: AlertTitle, hint: str | None = None) -> None:
        label = title.label(self.strings)
        log.error("%s: %s", label, error)
        message = "\n".join(part for part in (user_message(error, self.strings), hint) if part)
        self.platform.dialogs.show_alert(label, message)

    def _run(
        self,
        job: Callable[[], object],
        failure_title: AlertTitle | None = None,
        done: Callable[[Outcome], None] | None = None,
    ) -> None:
        def finished(outcome: Outcome) -> None:
            if not outcome.ok and failure_title is not None:
                self.report(outcome.error, failure_title)
            if done is not None:
                done(outcome)

        self._runner.run(job, finished)

    def _sample_traffic(self) -> None:
        """The counters are sampled at every change of the profiles, whatever page is
        open, so that the graph of a profile has its history when the page is opened."""
        self.traffic.record(self.store.profiles)
        self.traffic_changed.notify()
