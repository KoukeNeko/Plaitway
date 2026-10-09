"""The models that need no daemon: what the states mean, what the rows say, how the
numbers are written."""

import shutil
import tempfile
import unittest
from datetime import timezone
from pathlib import Path

import support  # noqa: F401  (the models log what they could not do; support keeps it out of the output)
from plaitway.client.errors import FailureKind
from plaitway.client.profile_set import ProfileSet, in_priority_order, moved, moved_by
from plaitway.client.types import (
    Diagnostics,
    LogLevel,
    LogLine,
    OwnedRoute,
    Profile,
    ProfileKind,
    ProfileState,
    RouteKind,
    RouteState,
    StaleRoute,
    TunnelMode,
)
from plaitway.core import formatting, presentation
from plaitway.core.aggregate import AggregateState
from plaitway.core.app_state import AppState, WindowGeometry
from plaitway.core.diagnostics_model import report_text
from plaitway.core.fakes import FakeScheduler
from plaitway.core.log_tail import CAPACITY, TRIM_SLACK, LogEntry, LogTail
from plaitway.core.ports import HelperRegistration
from plaitway.core.presentation import Presenter, RouteRow, DnsRow, Tone
from plaitway.core.profile_store import Connection
from plaitway.core.setup import (
    DaemonSetup,
    SetupAction,
    SetupState,
    menu_status,
    resolve_setup,
    setup_content,
)
from plaitway.core.traffic_history import CAPACITY as TRAFFIC_CAPACITY, TrafficHistory
from plaitway.core.tray_model import Mark, TrayAction, TrayMenu
from plaitway.l10n.localizer import detect_language, language_of
from plaitway.l10n.strings import Strings

STRINGS = Strings("en")


def make_profile(profile_id="p", name="Profile", state=ProfileState.DISCONNECTED, desired=False, kind=ProfileKind.OPENVPN):
    profile = Profile(id=profile_id, name=name, state=state, desired_enabled=desired, kind=kind)
    return profile


def setup(**options) -> DaemonSetup:
    defaults = dict(
        connection=Connection.UNAVAILABLE,
        registration=HelperRegistration.RUNNING,
        failure_kind=FailureKind.UNAVAILABLE,
        daemon_version=None,
        app_version="0.1.0",
        is_overridden=False,
        is_settling=False,
    )
    defaults.update(options)
    return resolve_setup(**defaults)


READY = DaemonSetup(SetupState.READY)
ALL_STATES = [state for state in SetupState]


class SetupTests(unittest.TestCase):
    def test_resolves(self):
        mismatch = DaemonSetup(SetupState.VERSION_MISMATCH, "0.0.9", "0.1.0")
        for options, expected in [
            (dict(connection=Connection.CONNECTED, daemon_version="0.1.0"), READY),
            (dict(connection=Connection.CONNECTED, daemon_version="0.0.9"), mismatch),
            (dict(connection=Connection.CONNECTED, daemon_version="0.0.0-dev", app_version=None), READY),
            (dict(connection=Connection.CONNECTED, daemon_version="0.0.0-dev", is_overridden=True), READY),
            (dict(connection=Connection.CONNECTED, registration=HelperRegistration.NOT_INSTALLED), READY),
            (dict(connection=Connection.CONNECTED, registration=None, daemon_version="0.1.0"), READY),
            # systemd has not been asked yet.
            (dict(registration=None), DaemonSetup(SetupState.CONNECTING)),
            (dict(registration=HelperRegistration.NOT_INSTALLED), DaemonSetup(SetupState.NOT_INSTALLED)),
            # A helper that something else started may be about to answer: not "not installed" yet.
            (dict(connection=Connection.CONNECTING, registration=HelperRegistration.NOT_INSTALLED), DaemonSetup(SetupState.CONNECTING)),
            (dict(registration=HelperRegistration.STOPPED), DaemonSetup(SetupState.NOT_RUNNING)),
            (dict(registration=HelperRegistration.STOPPED, is_settling=True), DaemonSetup(SetupState.CONNECTING)),
            (dict(registration=HelperRegistration.RUNNING), DaemonSetup(SetupState.NOT_RESPONDING)),
            (dict(registration=HelperRegistration.RUNNING, is_settling=True), DaemonSetup(SetupState.CONNECTING)),
            (dict(connection=Connection.CONNECTING, registration=HelperRegistration.RUNNING), DaemonSetup(SetupState.CONNECTING)),
            (dict(registration=HelperRegistration.UNKNOWN), DaemonSetup(SetupState.NOT_RESPONDING)),
            (dict(connection=Connection.CONNECTING, registration=HelperRegistration.UNKNOWN), DaemonSetup(SetupState.CONNECTING)),
            # The helper runs and refuses this user, or the app refuses its socket.
            (dict(failure_kind=FailureKind.PERMISSION_DENIED), DaemonSetup(SetupState.PERMISSION_DENIED)),
            (
                dict(failure_kind=FailureKind.SERVER_REFUSED, failure_message="/run/plaitway is writable by others"),
                DaemonSetup(SetupState.SERVER_REFUSED, detail="/run/plaitway is writable by others"),
            ),
            (dict(failure_kind=FailureKind.PERMISSION_DENIED, registration=None), DaemonSetup(SetupState.PERMISSION_DENIED)),
            # A development daemon is not the helper.
            (dict(is_overridden=True), DaemonSetup(SetupState.DEVELOPMENT)),
            (dict(is_overridden=True, connection=Connection.CONNECTING), DaemonSetup(SetupState.CONNECTING)),
            (dict(is_overridden=True, registration=HelperRegistration.NOT_INSTALLED), DaemonSetup(SetupState.DEVELOPMENT)),
        ]:
            with self.subTest(options=options):
                self.assertEqual(setup(**options), expected)

    def test_only_an_answering_daemon_is_usable(self):
        usable = {SetupState.READY, SetupState.VERSION_MISMATCH}
        for state in ALL_STATES:
            self.assertEqual(DaemonSetup(state).is_usable, state in usable, state)

    def test_one_thing_to_do_per_state(self):
        def content(state, **options):
            return setup_content(DaemonSetup(state, **options), STRINGS)

        self.assertEqual(content(SetupState.NOT_RUNNING).primary, SetupAction.START_HELPER)
        self.assertEqual(content(SetupState.NOT_INSTALLED).primary, SetupAction.RETRY)
        self.assertTrue(content(SetupState.NOT_INSTALLED).detail)
        self.assertEqual(content(SetupState.NOT_RESPONDING).primary, SetupAction.RETRY)
        self.assertEqual(content(SetupState.NOT_RESPONDING).secondary, SetupAction.RESTART_HELPER)
        self.assertEqual(content(SetupState.PERMISSION_DENIED).primary, SetupAction.RETRY)
        self.assertEqual(content(SetupState.SERVER_REFUSED, detail="x is writable").detail, "x is writable")
        self.assertEqual(content(SetupState.DEVELOPMENT).primary, SetupAction.RETRY)
        self.assertIsNone(content(SetupState.CONNECTING).primary)
        self.assertTrue(content(SetupState.CONNECTING).shows_progress)
        # The profiles are shown once the daemon answers, even when it is out of date.
        self.assertIsNone(content(SetupState.READY))
        self.assertIsNone(content(SetupState.VERSION_MISMATCH))

    def test_an_unanswered_helper_says_where_to_look(self):
        detail = setup_content(DaemonSetup(SetupState.NOT_RESPONDING), STRINGS).detail
        self.assertIn("journalctl -u plaitwayd", detail)

    def test_every_blocked_state_is_named_in_the_tray_menu(self):
        self.assertIsNone(menu_status(READY, STRINGS))
        for state in ALL_STATES:
            if state is not SetupState.READY:
                self.assertTrue(menu_status(DaemonSetup(state), STRINGS), state)

    def test_every_state_with_content_has_a_glyph_that_exists(self):
        for state in ALL_STATES:
            content = setup_content(DaemonSetup(state), STRINGS)
            if content:
                self.assertTrue(content.glyph.endswith("-symbolic"), state)

    def test_actions_have_labels_in_both_languages(self):
        for language in ("en", "zh-Hant"):
            for action in SetupAction:
                self.assertTrue(action.label(Strings(language)), (language, action))


class AggregateStateTests(unittest.TestCase):
    def test_summarises_the_profiles(self):
        S = ProfileState
        for states, expected in [
            ([], AggregateState.IDLE),
            ([S.DISCONNECTED, S.DISCONNECTED], AggregateState.IDLE),
            ([S.DISCONNECTED, S.CONNECTED], AggregateState.CONNECTED),
            ([S.CONNECTED, S.CONNECTING], AggregateState.CONNECTING),
            ([S.CONNECTED, S.RECONNECTING], AggregateState.CONNECTING),
            ([S.DISCONNECTED, S.AWAITING_CREDENTIALS], AggregateState.NEEDS_CREDENTIALS),
            ([S.CONNECTING, S.AWAITING_CREDENTIALS], AggregateState.NEEDS_CREDENTIALS),
            ([S.CONNECTED, S.AWAITING_CREDENTIALS], AggregateState.NEEDS_CREDENTIALS),
            ([S.FAILED, S.AWAITING_CREDENTIALS], AggregateState.PROBLEM),
            ([S.CONNECTED, S.DISCONNECTING], AggregateState.CONNECTING),
            ([S.CONNECTED, S.FAILED], AggregateState.PROBLEM),
            ([S.CONNECTING, S.FAILED], AggregateState.PROBLEM),
        ]:
            with self.subTest(states=states):
                profiles = [make_profile(f"p{i}", state=state) for i, state in enumerate(states)]
                self.assertEqual(AggregateState.of(READY, profiles), expected)
                self.assertEqual(AggregateState.of(DaemonSetup(SetupState.VERSION_MISMATCH), profiles), expected)

    def test_a_daemon_that_does_not_answer_hides_what_the_profiles_said(self):
        profiles = [make_profile(state=ProfileState.CONNECTED)]
        for state in ALL_STATES:
            if state in (SetupState.READY, SetupState.VERSION_MISMATCH, SetupState.CONNECTING):
                continue
            self.assertEqual(AggregateState.of(DaemonSetup(state), profiles), AggregateState.UNAVAILABLE, state)
        self.assertEqual(AggregateState.of(DaemonSetup(SetupState.CONNECTING), profiles), AggregateState.CONNECTING)

    def test_has_one_of_four_tray_icons_and_a_label(self):
        icons = {state: state.tray_icon for state in AggregateState}
        self.assertEqual(
            icons,
            {
                AggregateState.IDLE: "plaitway-state-idle-symbolic",
                AggregateState.CONNECTING: "plaitway-state-connecting-symbolic",
                AggregateState.CONNECTED: "plaitway-state-connected-symbolic",
                AggregateState.UNAVAILABLE: "plaitway-state-attention-symbolic",
                AggregateState.NEEDS_CREDENTIALS: "plaitway-state-attention-symbolic",
                AggregateState.PROBLEM: "plaitway-state-attention-symbolic",
            },
        )
        self.assertEqual(len(set(icons.values())), 4)
        for state in AggregateState:
            self.assertTrue(state.label(STRINGS))


class TrayMenuTests(unittest.TestCase):
    PROFILES = [
        make_profile("a", "Router", ProfileState.CONNECTED, True),
        make_profile("b", "Home", ProfileState.DISCONNECTED),
        make_profile("c", "Office", ProfileState.FAILED, True),
        make_profile("d", "Lab", ProfileState.DISCONNECTING, False),
        make_profile("e", "Cafe", ProfileState.AWAITING_CREDENTIALS, True),
        make_profile("f", "Depot", ProfileState.RECONNECTING, True),
    ]

    def test_lists_the_profiles_in_order_with_their_state(self):
        menu = TrayMenu.of(READY, self.PROFILES, STRINGS)
        self.assertIsNone(menu.status)
        self.assertTrue(menu.can_import)
        self.assertEqual([item.title for item in menu.items], ["Router", "Home", "Office", "Lab", "Cafe", "Depot"])
        self.assertEqual(
            [item.mark for item in menu.items], [Mark.ON, Mark.OFF, Mark.OFF, Mark.MIXED, Mark.MIXED, Mark.MIXED]
        )
        self.assertEqual(
            [item.subtitle for item in menu.items],
            [f"OpenVPN · {STRINGS.__getattribute__(name)}" for name in
             ("connected", "disconnected", "failed", "disconnecting", "awaiting_credentials", "reconnecting")],
        )
        self.assertEqual([item.is_enabled for item in menu.items], [True, True, True, False, True, True])
        self.assertEqual([item.profile_id for item in menu.items], list("abcdef"))

    def test_a_row_does_what_its_profile_needs(self):
        menu = TrayMenu.of(READY, self.PROFILES, STRINGS)
        # Connected and under way: choosing it switches the profile off. A failed one is switched
        # on already, so choosing it tries again; one that waits for a password opens the dialog.
        self.assertEqual(
            [item.action for item in menu.items],
            [TrayAction.DISCONNECT, TrayAction.CONNECT, TrayAction.RETRY, TrayAction.DISCONNECT,
             TrayAction.ANSWER_CREDENTIALS, TrayAction.DISCONNECT],
        )

    def test_summarises_how_many_are_connected(self):
        connected = TrayMenu.of(
            READY,
            [make_profile("a", state=ProfileState.CONNECTED), make_profile("b", state=ProfileState.CONNECTED), make_profile("c")],
            STRINGS,
        )
        self.assertEqual(connected.summary, "Connected: 2")
        # A profile that is still connecting does not take the count away from the ones that are not.
        mixed = TrayMenu.of(READY, [make_profile("a", state=ProfileState.CONNECTED), make_profile("b", state=ProfileState.CONNECTING)], STRINGS)
        self.assertEqual(mixed.summary, "Connected: 1")
        self.assertEqual(TrayMenu.of(READY, [make_profile("a")], STRINGS).summary, AggregateState.IDLE.label(STRINGS))
        self.assertEqual(
            TrayMenu.of(READY, [make_profile("a", state=ProfileState.FAILED)], STRINGS).summary, AggregateState.PROBLEM.label(STRINGS)
        )
        self.assertIsNone(TrayMenu.of(READY, [], STRINGS).summary)

    def test_offers_disconnect_all_only_while_something_is_switched_on(self):
        self.assertTrue(TrayMenu.of(READY, self.PROFILES, STRINGS).can_disconnect_all)
        self.assertFalse(TrayMenu.of(READY, [make_profile("a")], STRINGS).can_disconnect_all)
        self.assertFalse(TrayMenu.of(DaemonSetup(SetupState.NOT_RESPONDING), self.PROFILES, STRINGS).can_disconnect_all)

    def test_an_out_of_date_helper_still_works_and_says_so(self):
        outdated = DaemonSetup(SetupState.VERSION_MISMATCH, "0.0.9", "0.1.0")
        menu = TrayMenu.of(outdated, self.PROFILES, STRINGS)
        self.assertEqual(menu.status, menu_status(outdated, STRINGS))
        self.assertEqual(len(menu.items), len(self.PROFILES))
        self.assertTrue(menu.can_import)

    def test_disables_everything_when_the_helper_does_not_answer(self):
        for state in ALL_STATES:
            if state in (SetupState.READY, SetupState.VERSION_MISMATCH):
                continue
            with self.subTest(state=state):
                menu = TrayMenu.of(DaemonSetup(state), self.PROFILES, STRINGS)
                self.assertTrue(menu.status)
                self.assertEqual(menu.items, ())
                self.assertFalse(menu.can_import)


class ProfileOrderTests(unittest.TestCase):
    def test_moves_profiles_like_a_list_drag(self):
        ids = ["a", "b", "c", "d"]
        self.assertEqual(moved(ids, [3], 0), ["d", "a", "b", "c"])
        self.assertEqual(moved(ids, [0], 4), ["b", "c", "d", "a"])
        self.assertEqual(moved(ids, [1], 3), ["a", "c", "b", "d"])
        self.assertEqual(moved(ids, [0, 1], 4), ["c", "d", "a", "b"])

    def test_moves_one_profile_by_one_place(self):
        ids = ["a", "b", "c"]
        self.assertEqual(moved_by(ids, "b", -1), ["b", "a", "c"])
        self.assertEqual(moved_by(ids, "b", 1), ["a", "c", "b"])
        self.assertIsNone(moved_by(ids, "a", -1))
        self.assertIsNone(moved_by(ids, "c", 1))
        self.assertIsNone(moved_by(ids, "x", 1))

    def test_priority_is_the_daemons_and_equal_priorities_keep_their_place(self):
        profiles = []
        for name, priority in [("a", 2), ("b", 1), ("c", 2), ("d", 1)]:
            profile = make_profile(name)
            profile.settings.priority = priority
            profiles.append(profile)
        self.assertEqual([p.id for p in in_priority_order(profiles)], ["b", "d", "a", "c"])

    def test_a_set_changes_one_profile_at_a_time(self):
        a, b = make_profile("a"), make_profile("b")
        a.settings.priority, b.settings.priority = 1, 2
        profiles = ProfileSet([b, a])
        self.assertEqual(profiles.ids, ["a", "b"])
        renamed = make_profile("a", "Renamed")
        renamed.settings.priority = 1
        self.assertEqual(profiles.with_changed(renamed).get("a").name, "Renamed")
        self.assertEqual(profiles.get("a").name, "Profile", "the old set is not changed")
        added = make_profile("c")
        added.settings.priority = 3
        self.assertEqual(profiles.with_changed(added).ids, ["a", "b", "c"])
        self.assertEqual(profiles.without("a").ids, ["b"])
        self.assertIsNone(profiles.get("nope"))


class PresentationTests(unittest.TestCase):
    def test_route_rows_show_states_and_name_the_profile_that_shadows(self):
        profile = make_profile(name="Lab", state=ProfileState.CONNECTED)
        profile.status.routes.add(prefix="192.168.1.0/24", state=RouteState.INSTALLED)
        profile.status.routes.add(
            prefix="10.99.0.0/16", state=RouteState.SHADOWED, shadowed_by="other", detail="held by a profile with higher priority"
        )
        profile.status.routes.add(prefix="192.168.0.0/24", state=RouteState.BLOCKED)
        presenter = Presenter(STRINGS)

        rows = RouteRow.rows(profile, lambda i: "Office" if i == "other" else None, presenter)
        self.assertEqual([row.prefix for row in rows], ["192.168.1.0/24", "10.99.0.0/16", "192.168.0.0/24"])
        self.assertEqual([row.state for row in rows], [RouteState.INSTALLED, RouteState.SHADOWED, RouteState.BLOCKED])
        self.assertEqual(rows[0].state_label, "Installed")
        self.assertEqual(rows[1].state_label, "Shadowed by Office")
        self.assertEqual(rows[1].detail, "held by a profile with higher priority")
        self.assertEqual(rows[2].state_label, "Blocked by local network")

        # A profile that is gone is still named, by its id.
        self.assertEqual(RouteRow.rows(profile, lambda i: None, presenter)[1].state_label, "Shadowed by other")

    def test_a_disconnected_profile_shows_the_routes_it_names(self):
        profile = make_profile()
        profile.summary.routes.append("192.168.1.0/24")
        rows = RouteRow.rows(profile, lambda i: None, Presenter(STRINGS))
        self.assertEqual([row.prefix for row in rows], ["192.168.1.0/24"])
        self.assertIsNone(rows[0].state)
        self.assertIsNone(rows[0].state_label)

    def test_dns_rows_turn_the_dot_into_every_domain(self):
        profile = make_profile(state=ProfileState.CONNECTED)
        profile.status.dns.add(servers=["10.8.0.1", "10.8.0.2"], match_domains=["."], state=RouteState.INSTALLED)
        profile.status.dns.add(servers=["10.9.0.1"], match_domains=["corp.example", "lan"], state=RouteState.FAILED, detail="refused")
        rows = DnsRow.rows(profile, STRINGS)
        self.assertEqual([row.servers for row in rows], ["10.8.0.1, 10.8.0.2", "10.9.0.1"])
        self.assertEqual(rows[0].domains, "All domains")
        self.assertEqual(rows[1].domains, "corp.example, lan")
        self.assertEqual((rows[1].state, rows[1].detail), (RouteState.FAILED, "refused"))

    def test_every_state_has_a_label_in_both_languages(self):
        for language in ("en", "zh-Hant"):
            presenter = Presenter(Strings(language))
            for state in ProfileState:
                self.assertTrue(presenter.state_label(state), (language, state))
            for state in RouteState:
                self.assertTrue(presenter.route_state_label(state), (language, state))
            for kind in RouteKind:
                self.assertTrue(presenter.route_kind_label(kind), (language, kind))
            for mode in TunnelMode:
                self.assertTrue(presenter.tunnel_mode_label(mode), (language, mode))
        # Product names are not translated.
        self.assertEqual(Presenter.kind_label(ProfileKind.OPENVPN), "OpenVPN")
        self.assertEqual(Presenter.kind_label(ProfileKind.WIREGUARD), "WireGuard")
        self.assertEqual(presentation.TUNNEL_MODE_CHOICES, (TunnelMode.AUTO, TunnelMode.FULL, TunnelMode.SPLIT))

    def test_a_number_the_app_does_not_know_reads_as_unknown(self):
        presenter = Presenter(STRINGS)
        self.assertEqual(presenter.state_label(99), "Unknown")
        self.assertEqual(presenter.kind_text(99), "Unknown")
        self.assertEqual(presentation.profile_glyph(99), presentation.profile_glyph(ProfileState.UNSPECIFIED))

    def test_no_two_profile_states_share_a_shape(self):
        # Colour is the third channel: the glyph and the word already tell the states apart.
        shown = [s for s in ProfileState if s is not ProfileState.UNSPECIFIED]
        glyphs = [presentation.profile_glyph(state) for state in shown]
        self.assertEqual(len(set(glyphs)), len(glyphs))

    def test_a_standby_route_is_not_painted_as_a_warning(self):
        self.assertEqual(presentation.route_tone(RouteState.SHADOWED), Tone.MUTED)
        self.assertNotEqual(presentation.route_tone(RouteState.BLOCKED), presentation.route_tone(RouteState.SHADOWED))
        self.assertTrue(presentation.is_lost(RouteState.BLOCKED) and presentation.is_lost(RouteState.FAILED))
        self.assertFalse(any(presentation.is_lost(s) for s in (RouteState.SHADOWED, RouteState.INSTALLED, RouteState.PENDING)))

    def test_only_an_ending_state_moves(self):
        moving = [s for s in ProfileState if presentation.is_transitional(s)]
        self.assertEqual(moving, [ProfileState.CONNECTING, ProfileState.DISCONNECTING, ProfileState.RECONNECTING])

    def test_import_warnings_name_the_line_and_the_directive(self):
        from plaitway.client.types import ImportWarning

        presenter = Presenter(STRINGS)
        warning = ImportWarning(line=4, directive="up", message="removed, a profile cannot run programs")
        self.assertEqual(presenter.warning_summary(warning), "Line 4: up – removed, a profile cannot run programs")
        self.assertEqual(presenter.warning_summary(ImportWarning(message="odd")), "odd")

    def test_log_levels_are_named_the_same_in_every_language(self):
        self.assertEqual([presentation.log_tag(level) for level in LogLevel], ["INFO", "DEBUG", "INFO", "WARN", "ERROR"])
        self.assertEqual(presentation.log_tone(LogLevel.ERROR), Tone.ERROR)
        self.assertEqual(presentation.log_tone(LogLevel.DEBUG), Tone.MUTED)


class FormattingTests(unittest.TestCase):
    def test_formats_byte_counts_and_endpoints(self):
        self.assertEqual(formatting.format_bytes(0), "0 B")
        self.assertEqual(formatting.format_bytes(999), "999 B")
        # Decimal units, as the file manager counts them.
        self.assertEqual(formatting.format_bytes(1_500_000), "1.5 MB")
        self.assertEqual(formatting.format_bytes(1_000), "1.0 kB")
        # A counter the daemon reports at the top of its range does not overflow.
        self.assertTrue(formatting.format_bytes(2**64 - 1).endswith("EB"))
        self.assertEqual(formatting.format_endpoint("vpn.example.net", 1194, "tcp"), "vpn.example.net:1194 (tcp)")
        self.assertEqual(formatting.format_endpoint("vpn.example.net", 0, ""), "vpn.example.net")

    def test_nothing_is_spelled_out_in_any_language(self):
        for language in ("en", "zh-Hant"):
            strings = Strings(language)
            for text in (formatting.format_bytes(0), formatting.format_rate(0, strings), formatting.format_rate(0.4, strings)):
                self.assertTrue(any(char.isdigit() for char in text), (language, text))
                self.assertNotIn("zero", text.lower())
        self.assertTrue(formatting.format_rate(0, Strings("en")).endswith("/s"))
        self.assertTrue(formatting.format_rate(0, Strings("zh-Hant")).endswith("/秒"))
        self.assertIn("MB", formatting.format_rate(1_500_000, STRINGS))

    def test_the_uptime_counts_hours_as_there_are(self):
        self.assertEqual(formatting.format_uptime(0), "0:00:00")
        self.assertEqual(formatting.format_uptime(61), "0:01:01")
        self.assertEqual(formatting.format_uptime(3600 * 26 + 125), "26:02:05")
        self.assertEqual(formatting.format_uptime(-5), "0:00:00")

    def test_log_times_are_the_same_width(self):
        times = [formatting.format_log_time(seconds, timezone.utc) for seconds in (0, 3600 * 13 + 61, 86_399)]
        self.assertEqual(times, ["00:00:00", "13:01:01", "23:59:59"])

    def test_log_lines_are_copied_as_plain_text(self):
        self.assertEqual(LogEntry(1, None, LogLevel.WARN, "something odd").plain_text, "WARN something odd")
        timed = LogEntry(2, 0.0, LogLevel.INFO, "up").plain_text
        self.assertEqual(len(timed), len("00:00:00 INFO up"))
        self.assertTrue(timed.endswith(" INFO up"))


class TrafficHistoryTests(unittest.TestCase):
    START = 1_000.0

    @staticmethod
    def connected(received, sent, profile_id="a"):
        profile = make_profile(profile_id, state=ProfileState.CONNECTED)
        profile.status.rx_bytes = received
        profile.status.tx_bytes = sent
        return profile

    def test_works_out_the_rates_from_the_counters(self):
        history = TrafficHistory()
        history.record([self.connected(1_000, 100)], self.START)
        self.assertIsNone(history.rate("a"), "one reading says nothing about speed")
        history.record([self.connected(5_000, 300)], self.START + 2)
        rate = history.rate("a")
        self.assertEqual((rate.received, rate.sent), (2_000, 100))
        self.assertEqual(len(history.samples("a")), 1)

    def test_shows_the_mean_of_the_last_readings(self):
        history = TrafficHistory()
        # Counters every two seconds: 1000, 1000, 4000 and 4000 bytes per second.
        for index, counter in enumerate([0, 2_000, 4_000, 12_000, 20_000]):
            history.record([self.connected(counter, 0)], self.START + index * 2)
        self.assertEqual([s.received for s in history.samples("a")], [1_000, 1_000, 4_000, 4_000])
        self.assertEqual(history.rate("a").received, 3_000, "the last three samples only")

    def test_ignores_a_second_reading_for_the_same_interval(self):
        history = TrafficHistory()
        history.record([self.connected(0, 0)], self.START)
        history.record([self.connected(10, 0)], self.START + 0.2)
        self.assertEqual(history.samples("a"), [])
        # The interval is measured from the first reading, not from the one that was ignored.
        history.record([self.connected(2_000, 0)], self.START + 2)
        self.assertEqual(history.rate("a").received, 1_000)

    def test_a_reading_of_counters_that_have_not_moved_waits_until_the_tunnel_is_quiet(self):
        history = TrafficHistory()
        history.record([self.connected(1_000, 0)], self.START)
        # The same report read again (an event for another profile, the timer of a quiet app).
        history.record([self.connected(1_000, 0)], self.START + 1.2)
        history.record([self.connected(1_000, 0)], self.START + 2.4)
        self.assertEqual(history.samples("a"), [], "no sample of nothing between two reports")
        history.record([self.connected(3_000, 0)], self.START + 2.5)
        self.assertEqual(history.rate("a").received, 800, "the interval runs from the last reading that counted")
        # A tunnel that has said nothing for a while is quiet, and its rate is zero.
        history.record([self.connected(3_000, 0)], self.START + 5.0)
        history.record([self.connected(3_000, 0)], self.START + 5.6)
        self.assertEqual(history.samples("a")[-1].received, 0)

    def test_starts_again_when_a_counter_goes_down(self):
        history = TrafficHistory()
        history.record([self.connected(1_000, 0)], self.START)
        history.record([self.connected(3_000, 0)], self.START + 2)
        self.assertEqual(len(history.samples("a")), 1)
        history.record([self.connected(100, 0)], self.START + 4)
        self.assertEqual(history.samples("a"), [], "a reset says nothing about speed, and never a negative one")
        history.record([self.connected(2_100, 0)], self.START + 6)
        self.assertEqual(history.rate("a").received, 1_000)

    def test_forgets_a_profile_that_is_not_connected(self):
        history = TrafficHistory()
        history.record([self.connected(0, 0)], self.START)
        history.record([self.connected(2_000, 0)], self.START + 2)
        history.record([make_profile("a", state=ProfileState.RECONNECTING)], self.START + 4)
        self.assertEqual(history.samples("a"), [])
        history.record([self.connected(0, 0)], self.START + 6)
        self.assertIsNone(history.rate("a"), "a new connection has its own history")

    def test_keeps_two_minutes(self):
        history = TrafficHistory()
        for step in range(TRAFFIC_CAPACITY + 21):
            history.record([self.connected(step * 1_000, 0)], self.START + step * 2)
        self.assertEqual(len(history.samples("a")), TRAFFIC_CAPACITY)


class DiagnosticsReportTests(unittest.TestCase):
    def test_lists_what_a_bug_report_needs_and_no_log_lines(self):
        diagnostics = Diagnostics()
        diagnostics.network.default_gateway_v4 = "192.168.1.1"
        diagnostics.network.default_interface_v4 = "eth0"
        diagnostics.network.interfaces.extend(["lo", "eth0", "tun4"])
        diagnostics.owned_routes.append(
            OwnedRoute(prefix="10.20.0.0/16", owner="p1", via="tun4", state=RouteState.INSTALLED, kind=RouteKind.TUNNEL)
        )
        diagnostics.stale_routes.append(
            StaleRoute(prefix="203.0.113.9/32", gateway="192.168.0.254", interface="eth0",
                       reason="the gateway is not on the interface's subnet", owned=True)
        )
        diagnostics.resolver_entries.append("eth0 plaitway-p1 10.20.0.1")

        report = report_text(diagnostics, "0.2.0", None, lambda i: "Office" if i == "p1" else None, Presenter(STRINGS))

        self.assertTrue(report.startswith("Plaitway 0.2.0"))
        self.assertIn("gateway IPv4: 192.168.1.1 eth0", report)
        self.assertIn("10.20.0.0/16", report)
        self.assertIn("(Office)", report, "the profile is named, not its id")
        self.assertIn(
            "203.0.113.9/32 via 192.168.0.254 eth0: the gateway is not on the interface's subnet (installed by Plaitway)", report
        )
        self.assertIn("plaitway-p1", report)


class LogTailTests(unittest.TestCase):
    def line(self, text, level=LogLevel.INFO):
        return LogLine(text=text, level=level)

    def test_keeps_the_newest_lines_within_its_capacity(self):
        tail = LogTail(None, "", FakeScheduler())
        for number in range(CAPACITY + 500):
            tail.append(self.line(f"line {number}"))
        entries = tail.entries
        self.assertLessEqual(len(entries), CAPACITY + TRIM_SLACK)
        self.assertGreaterEqual(len(entries), CAPACITY)
        self.assertEqual(entries[-1].text, f"line {CAPACITY + 499}")
        self.assertEqual(len({entry.id for entry in entries}), len(entries), "line ids stay unique")

    def test_hands_out_only_what_is_new(self):
        tail = LogTail(None, "", FakeScheduler())
        for number in range(5):
            tail.append(self.line(str(number)))
        fresh, reset = tail.entries_after(-1)
        self.assertEqual(([e.text for e in fresh], reset), (list("01234"), False))
        tail.append(self.line("5"))
        fresh, reset = tail.entries_after(fresh[-1].id)
        self.assertEqual(([e.text for e in fresh], reset), (["5"], False))
        self.assertEqual(tail.entries_after(5), ([], False))

    def test_says_when_the_lines_a_view_shows_were_dropped(self):
        tail = LogTail(None, "", FakeScheduler())
        for number in range(CAPACITY + TRIM_SLACK + 1):
            tail.append(self.line(str(number)))
        fresh, reset = tail.entries_after(3)
        self.assertTrue(reset)
        self.assertEqual(fresh[-1].text, str(CAPACITY + TRIM_SLACK))
        fresh, reset = tail.entries_after(tail.entries[-1].id)
        self.assertEqual((fresh, reset), ([], False))

    def test_lines_are_copied_with_their_level(self):
        tail = LogTail(None, "", FakeScheduler())
        tail.append(self.line("first"))
        tail.append(self.line("second", LogLevel.ERROR))
        self.assertEqual(tail.plain_text, "INFO first\nERROR second")

    def test_a_burst_is_one_notification(self):
        scheduler = FakeScheduler()
        tail = LogTail(None, "", scheduler)
        heard = []
        tail.changed.connect(lambda: heard.append(len(tail.entries)))
        for number in range(100):
            tail.append(self.line(str(number)))
        self.assertEqual(heard, [], "the view is not drawn again for each line")
        scheduler.run_pending()
        self.assertEqual(heard, [100])


class LanguageTests(unittest.TestCase):
    def test_follows_the_desktop(self):
        for environment, language in [
            ({}, "en"),
            ({"LANG": "en_US.UTF-8"}, "en"),
            ({"LANG": "zh_TW.UTF-8"}, "zh-Hant"),
            ({"LANG": "zh_HK.UTF-8"}, "zh-Hant"),
            ({"LANG": "zh_Hant_TW"}, "zh-Hant"),
            ({"LANG": "zh-Hant"}, "zh-Hant"),
            ({"LANG": "zh_CN.UTF-8"}, "en"),
            ({"LANG": "zh_Hans"}, "en"),
            ({"LANG": "ja_JP.UTF-8"}, "en"),
            ({"LANG": "C"}, "en"),
            ({"LANG": "en_US.UTF-8", "LC_MESSAGES": "zh_TW.UTF-8"}, "zh-Hant"),
            ({"LANG": "en_US.UTF-8", "LC_ALL": "zh_TW.UTF-8"}, "zh-Hant"),
            ({"LANG": "zh_TW.UTF-8", "LC_ALL": "en_US.UTF-8"}, "en"),
            # LANGUAGE is a list: the first language the app has.
            ({"LANGUAGE": "ja:zh_TW:en", "LANG": "en_US.UTF-8"}, "zh-Hant"),
            ({"LANGUAGE": "zh_CN:en", "LANG": "zh_TW.UTF-8"}, "en"),
            ({"LANGUAGE": "de", "LANG": "zh_TW.UTF-8"}, "zh-Hant"),
            # The override wins.
            ({"PLAITWAY_LANGUAGE": "zh-Hant", "LANG": "en_US.UTF-8"}, "zh-Hant"),
            ({"PLAITWAY_LANGUAGE": "en", "LANG": "zh_TW.UTF-8", "LANGUAGE": "zh_TW"}, "en"),
            ({"PLAITWAY_LANGUAGE": "klingon", "LANG": "zh_TW.UTF-8"}, "zh-Hant"),
        ]:
            with self.subTest(environment=environment):
                self.assertEqual(detect_language(environment), language)

    def test_names_a_language_by_its_tag(self):
        self.assertEqual(language_of("zh_TW@modifier"), "zh-Hant")
        self.assertIsNone(language_of(""))
        self.assertIsNone(language_of("fr_FR"))


class AppStateTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.mkdtemp(prefix="pw-state-")
        self.addCleanup(shutil.rmtree, self.directory, True)
        self.path = Path(self.directory) / "plaitway" / "state.json"

    def test_remembers_the_window_and_the_tray_setting(self):
        state = AppState(self.path)
        self.assertEqual(state.window, WindowGeometry())
        self.assertTrue(state.show_tray_icon)
        state.set_window(WindowGeometry(900, 700, True))
        state.set_show_tray_icon(False)

        again = AppState(self.path)
        self.assertEqual(again.window, WindowGeometry(900, 700, True))
        self.assertFalse(again.show_tray_icon)

    def test_a_file_that_makes_no_sense_gives_the_defaults(self):
        self.path.parent.mkdir(parents=True)
        for content in ["not json", "[]", '{"window": {"width": "wide"}}', '{"window": {"width": -1, "height": 5}}', '{"show_tray_icon": 3}']:
            with self.subTest(content=content):
                self.path.write_text(content)
                state = AppState(self.path)
                self.assertEqual(state.window, WindowGeometry())

    def test_a_file_that_cannot_be_written_is_logged_not_fatal(self):
        blocker = Path(self.directory) / "blocked"
        blocker.write_text("a file where a directory should be")
        state = AppState(blocker / "state.json")
        with self.assertLogs("plaitway.core.app_state", "WARNING"):
            state.set_show_tray_icon(False)
        self.assertFalse(state.show_tray_icon)

    def test_lives_in_the_xdg_state_directory(self):
        from plaitway.core.app_state import default_path

        self.assertEqual(default_path({"XDG_STATE_HOME": "/x/state"}), Path("/x/state/plaitway/state.json"))
        self.assertTrue(str(default_path({})).endswith(".local/state/plaitway/state.json"))


if __name__ == "__main__":
    unittest.main()
