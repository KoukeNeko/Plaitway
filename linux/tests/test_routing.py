"""The Routing page's data: who holds each prefix, and the sentences about it."""

import unittest

import support  # noqa: F401
from plaitway.client.profile_set import ProfileSet
from plaitway.client.types import Profile, ProfileKind, ProfileSettings, ProfileState, RouteState, RouteStatus, TunnelStatus
from plaitway.core import routing
from plaitway.core.presentation import Presenter
from plaitway.core.routing import Standing
from plaitway.l10n.strings import Strings

STRINGS = Strings("en")
STATES = {"holds": RouteState.INSTALLED, "waits": RouteState.SHADOWED}


def profile(profile_id, priority, asks=(), state=ProfileState.CONNECTED, **fields):
    routes = [RouteStatus(prefix=prefix, state=STATES[standing]) for prefix, standing in asks]
    return Profile(
        id=profile_id, name=profile_id.upper(), kind=ProfileKind.OPENVPN, state=state, settings=ProfileSettings(priority=priority),
        status=TunnelStatus(routes=routes), **fields,
    )


def home_office_lab() -> ProfileSet:
    """Home holds the default route and 192.168.1.0/24; Office waits on both and holds 10.20.0.0/16."""
    return ProfileSet([
        profile("home", 0, [("0.0.0.0/0", "holds"), ("192.168.1.0/24", "holds")]),
        profile("office", 1, [("10.20.0.0/16", "holds"), ("0.0.0.0/0", "waits"), ("192.168.1.0/24", "waits")]),
        profile("lab", 2, [("172.16.8.0/24", "holds")]),
    ])


class RoutesTest(unittest.TestCase):
    def test_routes_group_requests_by_prefix_with_defaults_first(self):
        rows = routing.routes_of(home_office_lab())
        self.assertEqual([row.prefix for row in rows], ["0.0.0.0/0", "192.168.1.0/24", "10.20.0.0/16", "172.16.8.0/24"])
        self.assertEqual(rows[0].holder, 0)
        self.assertEqual(rows[0].requests, ((0, Standing.HOLDS), (1, Standing.WAITS)))
        self.assertEqual(rows[2].holder, 1)

    def test_a_profile_that_is_not_connected_asks_for_nothing(self):
        profiles = ProfileSet([profile("home", 0, state=ProfileState.DISCONNECTED)])
        self.assertEqual(routing.routes_of(profiles), [])

    def test_the_two_halves_of_a_default_route_are_one_route(self):
        halves = profile("a", 0, [("0.0.0.0/1", "holds"), ("10.0.0.0/8", "holds"), ("128.0.0.0/1", "holds")])
        self.assertEqual([row.prefix for row in routing.routes_of(ProfileSet([halves]))], ["0.0.0.0/0", "10.0.0.0/8"])
        apart = profile("b", 0, [("0.0.0.0/1", "waits"), ("128.0.0.0/1", "holds")])
        self.assertEqual(len(routing.routes_of(ProfileSet([apart]))), 2, "halves that stand differently are kept")

    def test_holds_counts_what_a_profile_won(self):
        profiles = home_office_lab()
        self.assertEqual(routing.holds(profiles[0]), (2, 2))
        self.assertEqual(routing.holds(profiles[1]), (1, 3))

    def test_a_colour_stays_with_the_profile_when_the_priorities_change(self):
        before = home_office_lab()
        after = ProfileSet([profile("home", 2), profile("office", 1), profile("lab", 0)])
        self.assertEqual(routing.hue_index(before, "home"), routing.hue_index(after, "home"))
        self.assertNotEqual(routing.hue_index(before, "home"), routing.hue_index(before, "office"))


class WordsTest(unittest.TestCase):
    def test_the_sentence_names_who_holds_and_who_waits(self):
        profiles = home_office_lab()
        rows = routing.routes_of(profiles)
        self.assertEqual(routing.why(rows[0], profiles, STRINGS), "Default route: HOME holds it, OFFICE waits behind")
        self.assertEqual(routing.why(rows[2], profiles, STRINGS), "10.20.0.0/16: OFFICE holds it")

    def test_a_route_nobody_holds_says_so(self):
        profiles = ProfileSet([profile("a", 0, [("10.0.0.0/8", "waits")])])
        row = routing.routes_of(profiles)[0]
        self.assertEqual(routing.why(row, profiles, STRINGS), "10.0.0.0/8: no tunnel holds it")

    def test_a_route_is_described_by_who_asks_for_it(self):
        profiles = home_office_lab()
        rows = routing.routes_of(profiles)
        self.assertEqual(routing.route_description(rows[0], profiles, STRINGS), "All internet traffic")
        self.assertEqual(routing.route_description(rows[1], profiles, STRINGS), "Asked by HOME, OFFICE")

    def test_the_state_line_starts_with_the_priority(self):
        profiles = home_office_lab()
        self.assertEqual(routing.state_line(profiles[1], 2, STRINGS), "Priority 2 · Holds 1 of 3 routes")
        idle = profile("x", 0, state=ProfileState.DISCONNECTED)
        self.assertEqual(routing.state_line(idle, 1, STRINGS), "Priority 1 · Not connected")
        failed = profile("x", 0, state=ProfileState.FAILED, last_error="no route to host")
        self.assertEqual(routing.state_line(failed, 1, STRINGS), "no route to host")

    def test_the_sidebar_says_what_a_connected_profile_holds_and_the_state_of_another(self):
        presenter = Presenter(STRINGS)
        profiles = home_office_lab()
        self.assertEqual(routing.caption(profiles[1], presenter), "OpenVPN · Holds 1 of 3 routes")
        idle = profile("x", 0, state=ProfileState.DISCONNECTED)
        self.assertEqual(routing.caption(idle, presenter), "OpenVPN · Disconnected")

    def test_the_chips_name_the_tunnel_and_how_it_stands(self):
        self.assertEqual(routing.chip_text(Standing.HOLDS, "Home", STRINGS), "Home holds")
        self.assertEqual(routing.chip_text(Standing.WAITS, "Office", STRINGS), "Office waits")


if __name__ == "__main__":
    unittest.main()
