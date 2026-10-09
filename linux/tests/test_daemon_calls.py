"""Every call of the client against the real daemon, on its in-memory backend."""

import base64
import time
import unittest

from plaitway.client.config_diagnostic import ConfigDiagnostic
from plaitway.client.errors import DaemonFailure, FailureKind
from plaitway.client.secret_mask import SecretMask
from plaitway.client.types import (
    CredentialKind,
    LogLevel,
    OnDemandRules,
    ProfileKind,
    ProfileSettings,
    ProfileState,
    RouteKind,
    RouteState,
    TunnelMode,
    on_demand_is_active,
)

from support import DaemonTestCase, openvpn_profile, wireguard_profile
from test_client import Recorder, wait_for


def text(data: bytes) -> str:
    return data.decode()


class CallTestCase(DaemonTestCase):
    def import_profile(self, filename: str, content: bytes = b"", **options) -> str:
        return self.client.import_profile(content or openvpn_profile(), filename, **options).profile.id

    def profile(self, profile_id: str):
        return next((p for p in self.client.list_profiles() if p.id == profile_id), None)

    def wait_for_state(self, profile_id: str, state) -> None:
        wait_for(lambda: (p := self.profile(profile_id)) is not None and p.state == state, f"{profile_id} to be {state!r}")

    def assert_fails(self, kind: FailureKind, call, *args, **kwargs) -> DaemonFailure:
        with self.assertRaises(DaemonFailure) as raised:
            call(*args, **kwargs)
        self.assertEqual(raised.exception.kind, kind)
        return raised.exception


class DaemonInfoTests(CallTestCase):
    def test_knows_the_daemon(self):
        info = self.client.get_daemon_info()
        self.assertTrue(info.privileged)
        self.assertEqual([engine.available for engine in info.engines], [True, True])
        self.assertTrue(info.version)

    def test_unknown_profile_is_not_found(self):
        self.assert_fails(FailureKind.NOT_FOUND, self.client.set_profile_enabled, "nope", True)


class ImportTests(CallTestCase):
    def test_imports_an_openvpn_profile(self):
        result = self.client.import_profile(openvpn_profile(host="vpn.example.net"), "/home/someone/Router.ovpn")
        profile = result.profile
        self.assertEqual(profile.kind, ProfileKind.OPENVPN)
        self.assertEqual(profile.name, "Router")
        self.assertEqual(profile.state, ProfileState.DISCONNECTED)
        self.assertEqual(profile.summary.endpoints[0].host, "vpn.example.net")
        self.assertEqual(profile.summary.endpoints[0].protocol, "tcp")
        self.assertEqual(list(profile.summary.routes), ["192.168.1.0/24"])
        self.assertEqual(result.warnings, [])

    def test_imports_a_wireguard_profile_and_knows_it_is_a_full_tunnel(self):
        result = self.client.import_profile(wireguard_profile(), "home.conf", name="Home router")
        profile = result.profile
        self.assertEqual(profile.kind, ProfileKind.WIREGUARD)
        self.assertEqual(profile.name, "Home router")
        self.assertTrue(profile.summary.redirects_default_route)
        self.assertEqual(list(profile.summary.addresses), ["10.6.0.2/32"])

    def test_returns_what_the_daemon_removed(self):
        result = self.client.import_profile(openvpn_profile(extra=["up /bin/sh"]), "scripted.ovpn")
        self.assertEqual([warning.directive for warning in result.warnings], ["up"])
        self.assertEqual(result.warnings[0].line, 6)
        self.assertTrue(result.warnings[0].message)

    def test_shows_the_reason_a_rejected_profile_gives(self):
        failure = self.assert_fails(
            FailureKind.REJECTED, self.client.import_profile, openvpn_profile(markers=["# fake: reject"]), "bad.ovpn"
        )
        self.assertIn("rejected", failure.message)
        self.assertEqual(self.client.list_profiles(), [])

    def test_refuses_empty_content(self):
        with self.assertRaises(DaemonFailure):
            self.client.import_profile(b"", "empty.ovpn")


class UpdateTests(CallTestCase):
    def test_updates_name_and_settings(self):
        profile_id = self.import_profile("office.ovpn")
        first = self.profile(profile_id).settings
        settings = ProfileSettings()
        settings.CopyFrom(first)
        settings.auto_connect = True
        settings.tunnel_mode = TunnelMode.SPLIT
        self.client.update_profile(profile_id, name="Office", settings=settings)

        profile = self.profile(profile_id)
        self.assertEqual(profile.name, "Office")
        self.assertTrue(profile.settings.auto_connect)
        self.assertEqual(profile.settings.tunnel_mode, TunnelMode.SPLIT)
        self.assertEqual(profile.settings.priority, first.priority)

    def test_updating_only_the_name_keeps_the_settings(self):
        settings = ProfileSettings(auto_connect=True, tunnel_mode=TunnelMode.FULL)
        profile_id = self.import_profile("office.ovpn", settings=settings)
        self.client.update_profile(profile_id, name="Renamed")
        profile = self.profile(profile_id)
        self.assertEqual(profile.name, "Renamed")
        self.assertTrue(profile.settings.auto_connect)
        self.assertEqual(profile.settings.tunnel_mode, TunnelMode.FULL)

    def test_updating_an_unknown_profile_is_not_found(self):
        self.assert_fails(FailureKind.NOT_FOUND, self.client.update_profile, "nope", name="x")

    def test_excluding_private_ips_is_stored_for_a_wireguard_profile(self):
        profile_id = self.import_profile("home.conf", wireguard_profile())
        self.assertFalse(self.profile(profile_id).settings.exclude_private_ips)
        settings = ProfileSettings()
        settings.CopyFrom(self.profile(profile_id).settings)
        settings.exclude_private_ips = True
        self.client.update_profile(profile_id, settings=settings)
        profile = self.profile(profile_id)
        self.assertTrue(profile.settings.exclude_private_ips)
        self.assertEqual(profile.settings.priority, settings.priority)

    def test_excluding_private_ips_is_refused_for_an_openvpn_profile(self):
        profile_id = self.import_profile("office.ovpn")
        settings = ProfileSettings()
        settings.CopyFrom(self.profile(profile_id).settings)
        settings.exclude_private_ips = True
        failure = self.assert_fails(FailureKind.REJECTED, self.client.update_profile, profile_id, settings=settings)
        self.assertEqual(failure.message, "excluding private IPs works on WireGuard's AllowedIPs, and this profile is OpenVPN")
        self.assertFalse(self.profile(profile_id).settings.exclude_private_ips)

    def test_on_demand_rules_are_stored_and_follow_the_network(self):
        profile_id = self.import_profile("office.ovpn")
        self.assertFalse(on_demand_is_active(self.profile(profile_id).settings.on_demand))

        # The fake daemon's network is Wi-Fi. Saving the rules applies them to it at once.
        settings = ProfileSettings()
        settings.CopyFrom(self.profile(profile_id).settings)
        settings.on_demand.wifi = True
        self.client.update_profile(profile_id, settings=settings)
        self.wait_for_state(profile_id, ProfileState.CONNECTED)
        rules = self.profile(profile_id).settings.on_demand
        self.assertTrue(rules.wifi)
        self.assertFalse(rules.ethernet)
        self.assertTrue(on_demand_is_active(rules))

        settings.CopyFrom(self.profile(profile_id).settings)
        settings.on_demand.wifi = False
        settings.on_demand.ethernet = True
        self.client.update_profile(profile_id, settings=settings)
        self.wait_for_state(profile_id, ProfileState.DISCONNECTED)
        self.assertTrue(self.profile(profile_id).settings.on_demand.ethernet)

    def test_clearing_both_rules_turns_on_demand_off(self):
        settings = ProfileSettings()
        settings.on_demand.ethernet = True
        profile_id = self.import_profile("office.ovpn", settings=settings)
        self.assertTrue(on_demand_is_active(self.profile(profile_id).settings.on_demand))
        settings.on_demand.CopyFrom(OnDemandRules())
        self.client.update_profile(profile_id, settings=settings)
        self.assertFalse(on_demand_is_active(self.profile(profile_id).settings.on_demand))

    def test_on_demand_is_active_when_either_network_is_checked(self):
        for ethernet, wifi, active in [(False, False, False), (True, False, True), (False, True, True), (True, True, True)]:
            self.assertEqual(on_demand_is_active(OnDemandRules(ethernet=ethernet, wifi=wifi)), active)


class DeleteAndReorderTests(CallTestCase):
    def test_reorders_by_priority(self):
        a, b, c = (self.import_profile(name) for name in ("a.ovpn", "b.ovpn", "c.ovpn"))
        self.assertEqual([p.id for p in self.client.list_profiles()], [a, b, c])
        self.client.reorder_profiles([c, a, b])
        self.assertEqual([p.id for p in self.client.list_profiles()], [c, a, b])
        self.assertEqual([p.settings.priority for p in self.client.list_profiles()], [1, 2, 3])

    def test_a_reorder_that_names_a_profile_twice_or_leaves_one_out_is_refused(self):
        a = self.import_profile("a.ovpn")
        b = self.import_profile("b.ovpn")
        with self.assertRaises(DaemonFailure):
            self.client.reorder_profiles([a])
        with self.assertRaises(DaemonFailure):
            self.client.reorder_profiles([a, a])
        self.assertEqual([p.id for p in self.client.list_profiles()], [a, b])

    def test_deletes_a_profile_and_disconnects_it_first(self):
        keep = self.import_profile("keep.ovpn")
        doomed = self.import_profile("doomed.ovpn")
        self.client.set_profile_enabled(doomed, True)
        self.wait_for_state(doomed, ProfileState.CONNECTED)
        self.client.delete_profile(doomed)
        self.assertEqual([p.id for p in self.client.list_profiles()], [keep])

    def test_deleting_an_unknown_profile_is_not_found(self):
        self.assert_fails(FailureKind.NOT_FOUND, self.client.delete_profile, "nope")


class ConnectTests(CallTestCase):
    def test_connects_several_profiles_at_the_same_time(self):
        ids = [self.import_profile("office.ovpn"), self.import_profile("lab.ovpn"), self.import_profile("home.conf", wireguard_profile())]
        for profile_id in ids:
            self.client.set_profile_enabled(profile_id, True)
        for profile_id in ids:
            self.wait_for_state(profile_id, ProfileState.CONNECTED)
        self.assertTrue(self.profile(ids[2]).status.interface_name)

    def test_reports_failure_with_an_error(self):
        broken = self.import_profile("broken.ovpn", openvpn_profile(markers=["# fake: fail"]))
        self.client.set_profile_enabled(broken, True)
        self.wait_for_state(broken, ProfileState.FAILED)
        self.assertTrue(self.profile(broken).last_error)

    def test_disabling_disconnects(self):
        office = self.import_profile("office.ovpn")
        self.client.set_profile_enabled(office, True)
        self.wait_for_state(office, ProfileState.CONNECTED)
        self.client.set_profile_enabled(office, False)
        self.wait_for_state(office, ProfileState.DISCONNECTED)
        self.assertFalse(self.profile(office).desired_enabled)

    def test_answering_without_a_request_is_refused(self):
        profile_id = self.import_profile("router.ovpn")
        self.assert_fails(
            FailureKind.REJECTED, self.client.provide_credentials, profile_id, CredentialKind.USER_PASSWORD, "alice", "s3cret"
        )


class ContentTests(CallTestCase):
    def test_reads_the_stored_text_not_what_was_uploaded(self):
        profile_id = self.import_profile("office.ovpn", openvpn_profile(extra=["up /bin/sh"]))
        self.assertEqual(self.client.get_profile_content(profile_id), text(openvpn_profile()))

    def test_keeps_a_byte_order_mark_the_text_starts_with(self):
        profile_id = self.import_profile("bom.ovpn", b"\xef\xbb\xbf" + openvpn_profile())
        self.assertTrue(self.client.get_profile_content(profile_id).startswith("﻿client"))

    def test_reading_an_unknown_profile_is_not_found(self):
        self.assert_fails(FailureKind.NOT_FOUND, self.client.get_profile_content, "nope")

    def test_replaces_the_text_and_refreshes_the_summary(self):
        profile_id = self.import_profile("home.conf", wireguard_profile())
        before = self.profile(profile_id).summary
        self.assertTrue(before.public_key)

        base = text(wireguard_profile(allowed_ips="10.9.0.0/16", endpoint="198.51.100.4:51821")).replace(
            "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC="
        )
        result = self.client.update_profile_content(profile_id, base + "PostUp = /bin/evil\n")

        self.assertEqual(self.client.get_profile_content(profile_id), base)
        summary = result.profile.summary
        self.assertEqual(result.profile.id, profile_id)
        self.assertEqual(summary.endpoints[0].host, "198.51.100.4")
        self.assertEqual(summary.endpoints[0].port, 51821)
        self.assertEqual(list(summary.routes), ["10.9.0.0/16"])
        self.assertTrue(summary.public_key)
        self.assertNotEqual(summary.public_key, before.public_key)
        self.assertEqual([warning.directive for warning in result.warnings], ["PostUp"])
        self.assertEqual(result.warnings[0].line, base.count("\n") + 1)
        wait_for(lambda: self.profile(profile_id).summary.public_key == summary.public_key, "the new summary")

    def test_updating_the_text_keeps_the_name_and_the_settings(self):
        settings = ProfileSettings(auto_connect=True, tunnel_mode=TunnelMode.SPLIT)
        profile_id = self.client.import_profile(openvpn_profile(), "office.ovpn", name="Office", settings=settings).profile.id
        result = self.client.update_profile_content(profile_id, text(openvpn_profile(host="edited.example.net")))
        self.assertEqual(result.profile.name, "Office")
        self.assertTrue(result.profile.settings.auto_connect)
        self.assertEqual(result.profile.settings.tunnel_mode, TunnelMode.SPLIT)
        self.assertEqual(result.profile.summary.endpoints[0].host, "edited.example.net")

    def test_text_of_the_other_kind_is_refused_with_the_daemons_message(self):
        office = self.import_profile("office.ovpn")
        home = self.import_profile("home.conf", wireguard_profile())
        for profile_id, content, message in [
            (office, text(wireguard_profile()), "this profile is OpenVPN, but the text is WireGuard"),
            (home, text(openvpn_profile()), "this profile is WireGuard, but the text is OpenVPN"),
        ]:
            with self.subTest(message=message):
                failure = self.assert_fails(FailureKind.REJECTED, self.client.update_profile_content, profile_id, content)
                self.assertEqual(failure.message, message)
                self.assertEqual(ConfigDiagnostic.from_error(failure), ConfigDiagnostic(None, message))
        self.assertEqual(self.client.get_profile_content(office), text(openvpn_profile()))
        self.assertEqual(self.client.get_profile_content(home), text(wireguard_profile()))

    def test_rejected_text_changes_nothing(self):
        profile_id = self.import_profile("office.ovpn")
        failure = self.assert_fails(
            FailureKind.REJECTED, self.client.update_profile_content, profile_id, text(openvpn_profile(markers=["# fake: reject"]))
        )
        self.assertTrue(failure.message.startswith("line 6: "))
        self.assertEqual(ConfigDiagnostic.from_error(failure).line, 6)
        self.assertEqual(self.client.get_profile_content(profile_id), text(openvpn_profile()))
        self.assertEqual(self.profile(profile_id).summary.endpoints[0].host, "vpn.example.net")

    def test_empty_text_is_refused(self):
        profile_id = self.import_profile("office.ovpn")
        with self.assertRaises(DaemonFailure):
            self.client.update_profile_content(profile_id, "")

    def test_updating_an_unknown_profile_is_not_found(self):
        self.assert_fails(FailureKind.NOT_FOUND, self.client.update_profile_content, "nope", text(openvpn_profile()))

    def test_the_new_text_of_a_running_profile_waits_for_the_next_connection(self):
        profile_id = self.import_profile("office.ovpn")
        self.client.set_profile_enabled(profile_id, True)
        self.wait_for_state(profile_id, ProfileState.CONNECTED)
        interface = self.profile(profile_id).status.interface_name

        # A text that makes the fake engine fail to connect, so a restart is visible.
        self.client.update_profile_content(profile_id, text(openvpn_profile(markers=["# fake: fail"])), reconnect=False)
        time.sleep(0.3)
        self.assertEqual(self.profile(profile_id).state, ProfileState.CONNECTED)
        self.assertEqual(self.profile(profile_id).status.interface_name, interface)

        self.client.set_profile_enabled(profile_id, False)
        self.wait_for_state(profile_id, ProfileState.DISCONNECTED)
        self.client.set_profile_enabled(profile_id, True)
        self.wait_for_state(profile_id, ProfileState.FAILED)

    def test_reconnect_restarts_an_enabled_profile_with_the_new_text(self):
        profile_id = self.import_profile("office.ovpn")
        self.client.set_profile_enabled(profile_id, True)
        self.wait_for_state(profile_id, ProfileState.CONNECTED)
        result = self.client.update_profile_content(profile_id, text(openvpn_profile(markers=["# fake: fail"])), reconnect=True)
        self.assertTrue(result.profile.desired_enabled)
        self.wait_for_state(profile_id, ProfileState.FAILED)

    def test_reconnect_leaves_a_disabled_profile_disconnected(self):
        profile_id = self.import_profile("office.ovpn")
        result = self.client.update_profile_content(profile_id, text(openvpn_profile(host="other.example.net")), reconnect=True)
        self.assertEqual(result.profile.state, ProfileState.DISCONNECTED)
        self.assertFalse(result.profile.desired_enabled)


class LogTests(CallTestCase):
    def watch_logs(self, profile_id: str) -> Recorder:
        recorder = Recorder()
        watch = self.client.watch_logs(profile_id, recorder)
        self.addCleanup(watch.cancel)
        return recorder

    def test_tails_a_profiles_log(self):
        profile_id = self.import_profile("office.ovpn")
        self.client.set_profile_enabled(profile_id, True)
        recorder = self.watch_logs(profile_id)
        line = wait_for(lambda: next((l for l in recorder.lines if l.text.startswith("connected on")), None), "the line")
        self.assertEqual(line.profile_id, profile_id)
        self.assertEqual(line.level, LogLevel.INFO)
        self.assertTrue(line.HasField("time"))

    def test_streams_live_lines_after_the_buffered_tail(self):
        profile_id = self.import_profile("office.ovpn")
        self.client.set_profile_enabled(profile_id, True)
        self.wait_for_state(profile_id, ProfileState.CONNECTED)
        recorder = self.watch_logs(profile_id)
        wait_for(lambda: any(l.text.startswith("connected on") for l in recorder.lines), "the buffered tail")
        # A line that did not exist when the stream was opened.
        self.client.set_profile_enabled(profile_id, False)
        stopping = wait_for(lambda: next((l for l in recorder.lines if l.text == "stopping"), None), "the live line")
        self.assertEqual(stopping.profile_id, profile_id)

    def test_tails_the_daemons_own_log_with_an_empty_profile_id(self):
        recorder = self.watch_logs("")
        line = wait_for(lambda: next((l for l in recorder.lines if "listening" in l.text), None), "the daemon's own line")
        self.assertEqual(line.profile_id, "")

    def test_a_log_of_an_unknown_profile_is_not_found(self):
        recorder = self.watch_logs("nope")
        wait_for(lambda: recorder.ended, "the end")
        self.assertEqual(recorder.ended[0].kind, FailureKind.NOT_FOUND)


class DiagnosticsTests(CallTestCase):
    """The stale route of the fake daemon is removed once, so these tests have the daemon to themselves."""

    def setUp(self):
        super().setUp()
        # A resync brings the stale route back.
        self.client.resync()

    def test_describes_the_network_and_the_stale_route(self):
        diagnostics = self.client.get_diagnostics()
        self.assertEqual(diagnostics.network.default_gateway_v4, "192.168.0.1")
        self.assertEqual(diagnostics.network.default_interface_v4, "en0")
        self.assertIn("en0", diagnostics.network.interfaces)
        self.assertEqual(list(diagnostics.owned_routes), [])
        self.assertEqual(diagnostics.daemon.version, self.client.get_daemon_info().version)

        self.assertEqual(len(diagnostics.stale_routes), 1)
        stale = diagnostics.stale_routes[0]
        self.assertEqual(stale.key, "fake-stale-1")
        self.assertEqual(stale.prefix, "203.0.113.9/32")
        self.assertEqual(stale.gateway, "192.168.0.254")
        self.assertEqual(stale.interface, "en0")
        self.assertTrue(stale.reason)

    def test_lists_the_routes_the_daemon_owns_with_their_states(self):
        profile_id = self.import_profile("office.ovpn", openvpn_profile(markers=["# fake: conflict"]))
        self.client.set_profile_enabled(profile_id, True)
        self.wait_for_state(profile_id, ProfileState.CONNECTED)

        diagnostics = self.client.get_diagnostics()
        routes = {route.prefix: route for route in diagnostics.owned_routes}
        self.assertEqual(routes["192.168.1.0/24"].state, RouteState.INSTALLED)
        self.assertEqual(routes["192.168.1.0/24"].kind, RouteKind.TUNNEL)
        self.assertEqual(routes["192.168.1.0/24"].owner, profile_id)
        self.assertEqual(routes["10.99.0.0/16"].state, RouteState.SHADOWED)
        self.assertEqual(routes["192.168.0.0/24"].state, RouteState.BLOCKED)
        self.assertTrue(any(profile_id in entry for entry in diagnostics.resolver_entries))

        # The profile's own view of the same routes names who shadows what.
        own = {route.prefix: route for route in self.profile(profile_id).status.routes}
        self.assertEqual(own["10.99.0.0/16"].state, RouteState.SHADOWED)
        self.assertEqual(own["10.99.0.0/16"].shadowed_by, "fake-peer")
        self.assertEqual(own["192.168.0.0/24"].state, RouteState.BLOCKED)

    def test_removes_a_stale_route_once(self):
        self.client.remove_stale_route("fake-stale-1")
        self.assertEqual(list(self.client.get_diagnostics().stale_routes), [])
        self.assert_fails(FailureKind.NOT_FOUND, self.client.remove_stale_route, "fake-stale-1")

    def test_resync_rebuilds_what_the_daemon_owns(self):
        profile_id = self.import_profile("office.ovpn")
        self.client.set_profile_enabled(profile_id, True)
        self.wait_for_state(profile_id, ProfileState.CONNECTED)
        self.client.remove_stale_route("fake-stale-1")

        self.client.resync()

        # The fake daemon reports the stale route again after a resync, and the connected profile
        # reconnects for a moment.
        diagnostics = self.client.get_diagnostics()
        self.assertEqual(diagnostics.network.last_change_reason, "manual")
        self.assertEqual([stale.key for stale in diagnostics.stale_routes], ["fake-stale-1"])
        self.wait_for_state(profile_id, ProfileState.RECONNECTING)
        self.wait_for_state(profile_id, ProfileState.CONNECTED)


def real_wireguard(private_key: str) -> str:
    """A WireGuard profile that the real parser accepts: its keys are real ones, which the fixture's are not."""
    public = base64.b64encode(bytes(range(32))).decode()
    return (
        f"[Interface]\nPrivateKey = {private_key}\nAddress = 10.6.0.2/32\n\n"
        f"[Peer]\nPublicKey = {public}\nAllowedIPs = 10.6.0.0/24\nEndpoint = 203.0.113.5:51820\n"
    )


class RealParserTests(CallTestCase):
    """What only the daemon's real profile parser decides: it runs without root, where it can
    parse and store profiles but not connect them."""

    fake = False

    def test_the_real_parser_names_the_line_it_rejects(self):
        profile_id = self.import_profile("office.ovpn")
        failure = self.assert_fails(
            FailureKind.REJECTED, self.client.update_profile_content, profile_id, text(openvpn_profile(extra=["route 10.0.0.0 notamask"]))
        )
        self.assertEqual(failure.message, 'line 6: route: "notamask" is not a netmask')
        self.assertEqual(ConfigDiagnostic.from_error(failure), ConfigDiagnostic(6, 'route: "notamask" is not a netmask'))

    def test_the_real_parser_names_the_line_of_a_wireguard_rejection(self):
        valid = real_wireguard(base64.b64encode(bytes(range(1, 33))).decode())
        profile_id = self.import_profile("home.conf", valid.encode())
        failure = self.assert_fails(FailureKind.REJECTED, self.client.update_profile_content, profile_id, real_wireguard("nope"))
        self.assertEqual(
            ConfigDiagnostic.from_error(failure), ConfigDiagnostic(2, "PrivateKey is not a base64-encoded 32-byte key")
        )

    def test_a_rejection_behind_a_masked_key_block_is_marked_on_the_line_the_editor_shows(self):
        # The daemon counts the lines of the text it is sent; the editor shows the masked one.
        original = "client\ndev tun\nremote vpn.example.net 1194\n<key>\n-----BEGIN PRIVATE KEY-----\nAAAA\nBBBB\n-----END PRIVATE KEY-----\n</key>\n"
        profile_id = self.import_profile("office.ovpn", original.encode())
        mask = SecretMask(self.client.get_profile_content(profile_id), ProfileKind.OPENVPN)
        self.assertIn("<key>\n‹secret 1›\n</key>", mask.display_text)

        edited = mask.display_text + "route 10.0.0.0 notamask\n"
        shown_line = edited.split("\n").index("route 10.0.0.0 notamask") + 1
        restoration = mask.restore(edited)
        failure = self.assert_fails(FailureKind.REJECTED, self.client.update_profile_content, profile_id, restoration.text)
        reported = ConfigDiagnostic.from_error(failure).line
        # The key block is four lines in the text the daemon read and one in the editor.
        self.assertEqual(reported, shown_line + 3)
        self.assertEqual(restoration.display_line(reported), shown_line)

    def test_the_public_key_is_derived_from_the_private_key(self):
        private = bytes(range(1, 33))
        profile_id = self.import_profile("home.conf", real_wireguard(base64.b64encode(private).decode()).encode())
        first = self.profile(profile_id).summary.public_key
        self.assertEqual(len(base64.b64decode(first)), 32)
        replacement = bytes(range(2, 34))
        result = self.client.update_profile_content(profile_id, real_wireguard(base64.b64encode(replacement).decode()))
        self.assertNotEqual(result.profile.summary.public_key, first)

    def test_pkcs12_is_refused_with_the_daemons_reason(self):
        failure = self.assert_fails(
            FailureKind.REJECTED, self.client.import_profile, b"client\nremote vpn.example.net 1194\npkcs12 client.p12\n", "router.ovpn"
        )
        self.assertIn("pkcs12", failure.message)


if __name__ == "__main__":
    unittest.main()
