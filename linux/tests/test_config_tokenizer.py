import random
import unittest

from plaitway.client.config_tokenizer import Role, tokens
from plaitway.client.types import ProfileKind

from support import repository_text

PRIVATE_KEY = "kPRIVATEkMATERIALkAAAAAAAAAAAAAAAAAAAAAAAA="
PUBLIC_KEY = "kPUBLICkKEYkCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC="
PRESHARED_KEY = "kPRESHAREDkMATERIALkBBBBBBBBBBBBBBBBBBBBBB="


def pieces(text: str, kind: int) -> list[tuple[str, Role]]:
    """Tokens as the text they cover and their role, which reads better in an expectation than a range."""
    return [(text[token.start : token.end], token.role) for token in tokens(text, kind)]


def wireguard(text: str):
    return pieces(text, ProfileKind.WIREGUARD)


def openvpn(text: str):
    return pieces(text, ProfileKind.OPENVPN)


class TokenAssertions(unittest.TestCase):
    def assert_well_formed(self, found, text: str) -> None:
        """What every token list has to satisfy, whatever the text: in order, inside the text,
        no overlap, no line breaks."""
        previous_end = 0
        for token in found:
            self.assertLess(token.start, token.end, f"{token} is empty")
            self.assertGreaterEqual(token.start, previous_end, f"{token} overlaps or precedes the token before it")
            self.assertLessEqual(token.end, len(text))
            if token.role is not Role.BLOCK_BODY:
                self.assertNotIn("\n", text[token.start : token.end], f"{token.role} spans a line break")
            previous_end = token.end


class WireGuardTokenizerTests(TokenAssertions):
    def test_tokens_a_wg_quick_profile(self):
        text = f"""# Home router
[Interface]
PrivateKey = {PRIVATE_KEY}
Address = 10.6.0.2/32, fd00:6::2/128
ListenPort = 51820
DNS = 10.6.0.1, 1.1.1.1, home.lan
MTU = 1380

[Peer]
PublicKey = {PUBLIC_KEY}
PresharedKey = {PRESHARED_KEY}
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = vpn.example.com:51820
PersistentKeepalive = 25
"""
        self.assertEqual(wireguard(text), [
            ("# Home router", Role.COMMENT),
            ("[Interface]", Role.SECTION),
            ("PrivateKey", Role.DIRECTIVE), (PRIVATE_KEY, Role.BASE64_KEY),
            ("Address", Role.DIRECTIVE), ("10.6.0.2/32", Role.CIDR), ("fd00:6::2/128", Role.CIDR),
            ("ListenPort", Role.DIRECTIVE), ("51820", Role.PORT),
            ("DNS", Role.DIRECTIVE), ("10.6.0.1", Role.IP_ADDRESS), ("1.1.1.1", Role.IP_ADDRESS), ("home.lan", Role.HOSTNAME),
            ("MTU", Role.DIRECTIVE), ("1380", Role.NUMBER),
            ("[Peer]", Role.SECTION),
            ("PublicKey", Role.DIRECTIVE), (PUBLIC_KEY, Role.BASE64_KEY),
            ("PresharedKey", Role.DIRECTIVE), (PRESHARED_KEY, Role.BASE64_KEY),
            ("AllowedIPs", Role.DIRECTIVE), ("0.0.0.0/0", Role.CIDR), ("::/0", Role.CIDR),
            ("Endpoint", Role.DIRECTIVE), ("vpn.example.com", Role.HOSTNAME), ("51820", Role.PORT),
            ("PersistentKeepalive", Role.DIRECTIVE), ("25", Role.NUMBER),
        ])
        self.assert_well_formed(tokens(text, ProfileKind.WIREGUARD), text)

    def test_splits_an_endpoint_into_host_and_port(self):
        for line, value in [
            ("Endpoint = 203.0.113.5:51820", [("203.0.113.5", Role.IP_ADDRESS), ("51820", Role.PORT)]),
            ("Endpoint = [2001:db8::1]:51820", [("2001:db8::1", Role.IP_ADDRESS), ("51820", Role.PORT)]),
            ("Endpoint = vpn.example.com:443", [("vpn.example.com", Role.HOSTNAME), ("443", Role.PORT)]),
            ("Endpoint = vpn.example.com", [("vpn.example.com", Role.ARGUMENT)]),
            ("Endpoint = vpn.example.com:https", [("vpn.example.com", Role.HOSTNAME), ("https", Role.ARGUMENT)]),
        ]:
            with self.subTest(line=line):
                self.assertEqual(wireguard(line), [("Endpoint", Role.DIRECTIVE), *value])

    def test_lists_are_split_at_the_commas_whatever_the_spacing(self):
        self.assertEqual(wireguard("AllowedIPs=0.0.0.0/1,128.0.0.0/1 ,  ::/0"), [
            ("AllowedIPs", Role.DIRECTIVE), ("0.0.0.0/1", Role.CIDR), ("128.0.0.0/1", Role.CIDR), ("::/0", Role.CIDR),
        ])
        self.assertEqual(wireguard("DNS = 1.1.1.1,, lan.example,"), [
            ("DNS", Role.DIRECTIVE), ("1.1.1.1", Role.IP_ADDRESS), ("lan.example", Role.HOSTNAME),
        ])
        self.assertEqual(wireguard("Address = 10.0.0.2"), [("Address", Role.DIRECTIVE), ("10.0.0.2", Role.IP_ADDRESS)])
        self.assertEqual(wireguard("AllowedIPs = 10.0.0.0/eight, 10.0.0.256/8"), [
            ("AllowedIPs", Role.DIRECTIVE), ("10.0.0.0/eight", Role.ARGUMENT), ("10.0.0.256/8", Role.ARGUMENT),
        ])

    def test_keys_are_matched_without_regard_for_case_and_spacing(self):
        self.assertEqual(wireguard("\tlistenport\t=\t51820\t"), [("listenport", Role.DIRECTIVE), ("51820", Role.PORT)])
        self.assertEqual(wireguard("PERSISTENTKEEPALIVE=off"), [("PERSISTENTKEEPALIVE", Role.DIRECTIVE), ("off", Role.ARGUMENT)])
        self.assertEqual(wireguard("Table = off"), [("Table", Role.DIRECTIVE), ("off", Role.ARGUMENT)])
        self.assertEqual(wireguard("FwMark = 0x1234"), [("FwMark", Role.DIRECTIVE), ("0x1234", Role.ARGUMENT)])
        self.assertEqual(wireguard("PostUp = iptables -A FORWARD -i %i -j ACCEPT"), [
            ("PostUp", Role.DIRECTIVE), ("iptables -A FORWARD -i %i -j ACCEPT", Role.ARGUMENT),
        ])
        self.assertEqual(wireguard(f"publickey={PUBLIC_KEY}"), [("publickey", Role.DIRECTIVE), (PUBLIC_KEY, Role.BASE64_KEY)])

    def test_a_comment_runs_from_the_hash_to_the_end_of_the_line(self):
        self.assertEqual(wireguard("Address = 10.0.0.2/32 # the LAN, 10.0.0.0/8 = nothing"), [
            ("Address", Role.DIRECTIVE), ("10.0.0.2/32", Role.CIDR), ("# the LAN, 10.0.0.0/8 = nothing", Role.COMMENT),
        ])
        self.assertEqual(wireguard("[Peer]# laptop"), [("[Peer]", Role.SECTION), ("# laptop", Role.COMMENT)])
        self.assertEqual(wireguard(f"  #PrivateKey = {PRIVATE_KEY}"), [(f"#PrivateKey = {PRIVATE_KEY}", Role.COMMENT)])
        self.assertEqual(wireguard("PrivateKey = # none yet"), [("PrivateKey", Role.DIRECTIVE), ("# none yet", Role.COMMENT)])

    def test_a_line_that_is_not_a_directive_has_no_role(self):
        self.assertEqual(wireguard("just some words\n\n   \n"), [])
        self.assertEqual(wireguard("=value"), [("value", Role.ARGUMENT)])
        self.assertEqual(wireguard("[Interface"), [("[Interface", Role.SECTION)])

    def test_lines_ending_in_crlf_have_no_cr_in_their_tokens(self):
        text = "[Interface]\r\nAddress = 10.0.0.2/32 # lan\r\nDNS = 1.1.1.1\r\n"
        self.assertEqual(wireguard(text), [
            ("[Interface]", Role.SECTION),
            ("Address", Role.DIRECTIVE), ("10.0.0.2/32", Role.CIDR), ("# lan", Role.COMMENT),
            ("DNS", Role.DIRECTIVE), ("1.1.1.1", Role.IP_ADDRESS),
        ])

    def test_a_byte_order_mark_at_the_start_belongs_to_no_token(self):
        self.assertEqual(wireguard("﻿[Interface]\nAddress = 10.0.0.2/32\n"), [
            ("[Interface]", Role.SECTION), ("Address", Role.DIRECTIVE), ("10.0.0.2/32", Role.CIDR),
        ])
        self.assertEqual(openvpn("﻿client\nverb 3\n"), [("client", Role.DIRECTIVE), ("verb", Role.DIRECTIVE), ("3", Role.NUMBER)])

    def test_text_in_other_scripts_keeps_its_ranges(self):
        text = "# 備註 🌐\nAddress = 10.0.0.2/32 # 筆電\nEndpoint = 例え.example:51820\n"
        self.assertEqual(wireguard(text), [
            ("# 備註 🌐", Role.COMMENT),
            ("Address", Role.DIRECTIVE), ("10.0.0.2/32", Role.CIDR), ("# 筆電", Role.COMMENT),
            ("Endpoint", Role.DIRECTIVE), ("例え.example", Role.HOSTNAME), ("51820", Role.PORT),
        ])


class OpenVPNTokenizerTests(TokenAssertions):
    def test_tokens_a_profile_line_by_line(self):
        text = """# Name: Office
client
dev tun
proto tcp-client
remote vpn.example.net 1194 ; the main server
remote 203.0.113.7 443 udp
keepalive 10 30
route 192.168.1.0 255.255.255.0
route-ipv6 2001:db8::/32
dhcp-option DNS 192.168.1.1
pull-filter ignore "redirect-gateway"
port 1194
<ca>
-----BEGIN CERTIFICATE-----
AAAA
-----END CERTIFICATE-----
</ca>
verb 3
"""
        self.assertEqual(openvpn(text), [
            ("# Name: Office", Role.COMMENT),
            ("client", Role.DIRECTIVE),
            ("dev", Role.DIRECTIVE), ("tun", Role.ARGUMENT),
            ("proto", Role.DIRECTIVE), ("tcp-client", Role.ARGUMENT),
            ("remote", Role.DIRECTIVE), ("vpn.example.net", Role.HOSTNAME), ("1194", Role.PORT), ("; the main server", Role.COMMENT),
            ("remote", Role.DIRECTIVE), ("203.0.113.7", Role.IP_ADDRESS), ("443", Role.PORT), ("udp", Role.ARGUMENT),
            ("keepalive", Role.DIRECTIVE), ("10", Role.NUMBER), ("30", Role.NUMBER),
            ("route", Role.DIRECTIVE), ("192.168.1.0", Role.IP_ADDRESS), ("255.255.255.0", Role.IP_ADDRESS),
            ("route-ipv6", Role.DIRECTIVE), ("2001:db8::/32", Role.CIDR),
            ("dhcp-option", Role.DIRECTIVE), ("DNS", Role.ARGUMENT), ("192.168.1.1", Role.IP_ADDRESS),
            ("pull-filter", Role.DIRECTIVE), ("ignore", Role.ARGUMENT), ('"redirect-gateway"', Role.ARGUMENT),
            ("port", Role.DIRECTIVE), ("1194", Role.PORT),
            ("<ca>", Role.BLOCK_TAG),
            ("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----", Role.BLOCK_BODY),
            ("</ca>", Role.BLOCK_TAG),
            ("verb", Role.DIRECTIVE), ("3", Role.NUMBER),
        ])
        self.assert_well_formed(tokens(text, ProfileKind.OPENVPN), text)

    def test_a_comment_starts_where_a_parameter_would(self):
        self.assertEqual(openvpn("# one\n; two\n  # three\n\t; four"), [
            ("# one", Role.COMMENT), ("; two", Role.COMMENT), ("# three", Role.COMMENT), ("; four", Role.COMMENT),
        ])
        # In the middle of a parameter `#` and `;` are just characters.
        self.assertEqual(openvpn("dhcp-option DOMAIN lan#x;y"), [
            ("dhcp-option", Role.DIRECTIVE), ("DOMAIN", Role.ARGUMENT), ("lan#x;y", Role.ARGUMENT),
        ])
        self.assertEqual(openvpn("verb 3 # chatty"), [("verb", Role.DIRECTIVE), ("3", Role.NUMBER), ("# chatty", Role.COMMENT)])
        self.assertEqual(openvpn("verb 3 #chatty;still the comment"), [
            ("verb", Role.DIRECTIVE), ("3", Role.NUMBER), ("#chatty;still the comment", Role.COMMENT),
        ])

    def test_quoted_parameters_are_one_token_with_their_quotes(self):
        self.assertEqual(openvpn('remote "vpn.example.net" 1194'), [
            ("remote", Role.DIRECTIVE), ('"vpn.example.net"', Role.HOSTNAME), ("1194", Role.PORT),
        ])
        self.assertEqual(openvpn("""setenv NAME "two words # not a comment" 'single ; quoted'"""), [
            ("setenv", Role.DIRECTIVE), ("NAME", Role.ARGUMENT),
            ('"two words # not a comment"', Role.ARGUMENT), ("'single ; quoted'", Role.ARGUMENT),
        ])
        self.assertEqual(openvpn('push "a \\" quote" next'), [
            ("push", Role.DIRECTIVE), ('"a \\" quote"', Role.ARGUMENT), ("next", Role.ARGUMENT),
        ])
        # A quote that is never closed runs to the end of the line.
        self.assertEqual(openvpn('push "open\nverb 3'), [
            ("push", Role.DIRECTIVE), ('"open', Role.ARGUMENT), ("verb", Role.DIRECTIVE), ("3", Role.NUMBER),
        ])

    def test_reads_the_host_and_the_port_of_a_server_directive(self):
        for written in ["remote", "--remote", "http-proxy", "socks-proxy"]:
            with self.subTest(written=written):
                self.assertEqual(openvpn(f"{written} proxy.example.net 8080"), [
                    (written, Role.DIRECTIVE), ("proxy.example.net", Role.HOSTNAME), ("8080", Role.PORT),
                ])
                self.assertEqual(openvpn(f"{written} 2001:db8::10 8080"), [
                    (written, Role.DIRECTIVE), ("2001:db8::10", Role.IP_ADDRESS), ("8080", Role.PORT),
                ])

    def test_a_block_body_is_one_token_whatever_it_holds(self):
        text = "<tls-crypt>\n#\n-----BEGIN OpenVPN Static key V1-----\n  ffee  \n\nremote not a directive 1\n-----END OpenVPN Static key V1-----\n</tls-crypt>\n"
        self.assertEqual(openvpn(text), [
            ("<tls-crypt>", Role.BLOCK_TAG),
            ("#\n-----BEGIN OpenVPN Static key V1-----\n  ffee  \n\nremote not a directive 1\n-----END OpenVPN Static key V1-----", Role.BLOCK_BODY),
            ("</tls-crypt>", Role.BLOCK_TAG),
        ])

    def test_the_tags_of_a_block_are_found_whatever_the_spacing_around_them(self):
        text = "  <key>  \t\n  body  \n\t</key>   \n<TLS-AUTH> # static key\nkey data\n</tls-auth>"
        self.assertEqual(openvpn(text), [
            ("<key>", Role.BLOCK_TAG),
            ("  body  ", Role.BLOCK_BODY),
            ("</key>", Role.BLOCK_TAG),
            ("<TLS-AUTH>", Role.BLOCK_TAG), ("# static key", Role.COMMENT),
            ("key data", Role.BLOCK_BODY),
            ("</tls-auth>", Role.BLOCK_TAG),
        ])

    def test_a_block_without_a_body_has_no_body_token(self):
        self.assertEqual(openvpn("<ca>\n</ca>\nverb 3\n"), [
            ("<ca>", Role.BLOCK_TAG), ("</ca>", Role.BLOCK_TAG), ("verb", Role.DIRECTIVE), ("3", Role.NUMBER),
        ])

    def test_a_block_that_is_never_closed_runs_to_the_end(self):
        self.assertEqual(openvpn("verb 3\n<ca>\nAAAA\nverb 4\n"), [
            ("verb", Role.DIRECTIVE), ("3", Role.NUMBER), ("<ca>", Role.BLOCK_TAG), ("AAAA\nverb 4", Role.BLOCK_BODY),
        ])

    def test_a_tag_in_a_comment_or_another_block_opens_nothing(self):
        self.assertEqual(openvpn("# <ca>\nverb 3\n"), [("# <ca>", Role.COMMENT), ("verb", Role.DIRECTIVE), ("3", Role.NUMBER)])
        self.assertEqual(openvpn("<ca>\n<key>\n</ca>\n"), [("<ca>", Role.BLOCK_TAG), ("<key>", Role.BLOCK_BODY), ("</ca>", Role.BLOCK_TAG)])
        # Two parameters are not a tag.
        self.assertEqual(openvpn("<ca> extra\n"), [("<ca>", Role.DIRECTIVE), ("extra", Role.ARGUMENT)])

    def test_the_lines_of_a_connection_block_are_directives(self):
        text = repository_text("internal/ovpn/testdata/connection-blocks.ovpn")
        found = openvpn(text)
        self.assertEqual(
            [piece for piece, role in found if role is Role.BLOCK_TAG],
            ["<connection>", "</connection>", "<connection>", "</connection>", "<connection>", "</connection>", "<ca>", "</ca>"],
        )
        self.assertIn(("primary.example.com", Role.HOSTNAME), found)
        self.assertIn(("2001:db8::10", Role.IP_ADDRESS), found)
        self.assertIn(("8443", Role.PORT), found)
        self.assertEqual(
            [piece for piece, role in found if role is Role.BLOCK_BODY],
            ["-----BEGIN CERTIFICATE-----\nY29ubmVjdGlvbiBjYQ==\n-----END CERTIFICATE-----"],
        )

    def test_a_crlf_profile_has_no_cr_at_the_end_of_any_token(self):
        text = repository_text("internal/ovpn/testdata/windows.ovpn")
        self.assertIn("\r\n", text)
        found = openvpn(text)
        self.assertTrue(all(not piece.endswith("\r") and not piece.startswith("\r") for piece, _ in found))
        for expected in [("remote", Role.DIRECTIVE), ("vpn.example.org", Role.HOSTNAME), ("443", Role.PORT)]:
            self.assertIn(expected, found)
        self.assertEqual(found[0], ("# Windows-style export, CRLF line endings", Role.COMMENT))
        key = next(piece for piece, role in found if role is Role.BLOCK_BODY and "PRIVATE KEY" in piece)
        self.assertEqual(key, "-----BEGIN PRIVATE KEY-----\r\nZmFrZSBrZXk=\r\n-----END PRIVATE KEY-----")

    def test_a_stray_closing_tag_is_a_tag(self):
        self.assertEqual(openvpn("</connection>\n"), [("</connection>", Role.BLOCK_TAG)])

    def test_tokens_the_router_profiles(self):
        asus = openvpn(repository_text("internal/ovpn/testdata/asus.ovpn"))
        self.assertEqual(asus[0], ("# Name: ASUS Router", Role.COMMENT))
        for piece in [
            ("auth-user-pass", Role.DIRECTIVE), ("vpn.example.net", Role.HOSTNAME), ("1194", Role.PORT), ("tcp-client", Role.ARGUMENT),
            ('"redirect-gateway"', Role.ARGUMENT), ("192.168.1.0", Role.IP_ADDRESS), ("255.255.255.0", Role.IP_ADDRESS),
            ("AES-128-CBC", Role.ARGUMENT),
        ]:
            self.assertIn(piece, asus)
        self.assertEqual(
            [piece for piece, role in asus if role is Role.BLOCK_TAG], ["<ca>", "</ca>", "<cert>", "</cert>", "<key>", "</key>"]
        )
        self.assertEqual(sum(1 for _, role in asus if role is Role.BLOCK_BODY), 3)

        merlin = openvpn(repository_text("internal/ovpn/testdata/merlin.ovpn"))
        for piece in [
            ("# Exported from an ASUSWRT-Merlin router", Role.COMMENT), ("203.0.113.7", Role.IP_ADDRESS), ("udp4", Role.ARGUMENT),
            ("AES-256-GCM:AES-128-GCM:AES-128-CBC", Role.ARGUMENT), ("192.168.1.1", Role.IP_ADDRESS), ("def1", Role.ARGUMENT),
        ]:
            self.assertIn(piece, merlin)
        self.assertEqual(sum(1 for _, role in merlin if role is Role.BLOCK_BODY), 4)


class RobustnessTests(TokenAssertions):
    def test_no_tokens_for_an_unknown_kind(self):
        self.assertEqual(tokens("client\nremote a 1\n", ProfileKind.UNSPECIFIED), [])
        self.assertEqual(tokens("[Interface]\n", 9), [])
        self.assertEqual(tokens("", ProfileKind.OPENVPN), [])
        self.assertEqual(tokens("", ProfileKind.WIREGUARD), [])

    def test_the_repository_profiles_are_well_formed(self):
        for name in ["asus", "merlin", "windows", "connection-blocks"]:
            with self.subTest(name=name):
                text = repository_text(f"internal/ovpn/testdata/{name}.ovpn")
                self.assert_well_formed(tokens(text, ProfileKind.OPENVPN), text)
                # A profile of the wrong kind is still read without trouble.
                self.assert_well_formed(tokens(text, ProfileKind.WIREGUARD), text)

    def test_any_text_gives_well_formed_tokens(self):
        alphabet = list("<>[]=#; \"'\\\t\r\n\n/:,.-_abcdefXYZ019é備🌐‹›")
        for seed in range(300):
            rng = random.Random(seed)
            text = "".join(rng.choice(alphabet) for _ in range(rng.randint(0, 240)))
            for kind in (ProfileKind.OPENVPN, ProfileKind.WIREGUARD):
                with self.subTest(seed=seed, kind=kind):
                    self.assert_well_formed(tokens(text, kind), text)


if __name__ == "__main__":
    unittest.main()
