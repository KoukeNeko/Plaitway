import random
import time
import unittest

from plaitway.client.secret_mask import DuplicatedPlaceholder, SecretMask, placeholder_ranges
from plaitway.client.types import ProfileKind

from support import repository_text

PRIVATE_KEY = "kPRIVATEkMATERIALkAAAAAAAAAAAAAAAAAAAAAAAA="
PRESHARED_KEY = "kPRESHAREDkMATERIALkBBBBBBBBBBBBBBBBBBBBBB="

WIREGUARD_PROFILE = f"""[Interface]
PrivateKey = {PRIVATE_KEY}
Address = 10.6.0.2/32
DNS = 10.6.0.1

[Peer]
PublicKey = kPUBLICkKEYkCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=
PresharedKey = {PRESHARED_KEY}
AllowedIPs = 0.0.0.0/0
Endpoint = 203.0.113.5:51820
"""


def wireguard(text: str) -> SecretMask:
    return SecretMask(text, ProfileKind.WIREGUARD)


def openvpn(text: str) -> SecretMask:
    return SecretMask(text, ProfileKind.OPENVPN)


def restored(mask: SecretMask, edited: str) -> str:
    return mask.restore(edited).text


class WireGuardMaskTests(unittest.TestCase):
    def test_hides_the_keys_of_a_wireguard_profile(self):
        mask = wireguard(WIREGUARD_PROFILE)
        self.assertEqual(
            mask.display_text,
            WIREGUARD_PROFILE.replace(PRIVATE_KEY, "‹secret 1›").replace(PRESHARED_KEY, "‹secret 2›"),
        )
        self.assertEqual(restored(mask, mask.display_text), WIREGUARD_PROFILE)

    def test_finds_the_value_whatever_the_spacing_and_the_trailing_comment(self):
        for line in [
            "PrivateKey=§",
            "privatekey = §",
            "PRIVATEKEY\t=\t§",
            "   PrivateKey   =   §   ",
            "PrivateKey = §# no space before the comment",
            "PrivateKey = §   # a comment",
        ]:
            with self.subTest(line=line):
                text = f"[Interface]\n{line}\nAddress = 10.0.0.2/32\n"
                mask = wireguard(text.replace("§", PRIVATE_KEY))
                self.assertNotIn(PRIVATE_KEY, mask.display_text)
                self.assertIn("Address = 10.0.0.2/32", mask.display_text)
                # Only the value is hidden, not what stands around it.
                self.assertEqual(mask.display_text, text.replace("§", "‹secret 1›"))
                self.assertEqual(restored(mask, mask.display_text), text.replace("§", PRIVATE_KEY))

    def test_a_key_behind_unicode_white_space_is_hidden(self):
        # The daemon trims a line with Go's strings.TrimSpace, which takes in the no-break
        # space, the ideographic space and the rest of Unicode white space.
        for space in [" ", "　", " ", "\u0085", "\u000b", "\u000c", " ", " "]:
            for name in ["PrivateKey", "PresharedKey"]:
                for text in [
                    f"[Interface]\n{space}{name} = {PRIVATE_KEY}\n",
                    f"[Interface]\n{name}{space}={space}{PRIVATE_KEY}{space}\n",
                    f"[Interface]\n#{space}{name}{space}={PRIVATE_KEY}\n",
                ]:
                    with self.subTest(space=hex(ord(space)), name=name, text=text):
                        mask = wireguard(text)
                        self.assertNotIn(PRIVATE_KEY, mask.display_text)
                        self.assertIn("‹secret 1›", mask.display_text)
                        self.assertEqual(restored(mask, mask.display_text), text)

    def test_a_comment_after_the_key_stays_visible(self):
        mask = wireguard(f"PrivateKey = {PRIVATE_KEY} # laptop key\n")
        self.assertEqual(mask.display_text, "PrivateKey = ‹secret 1› # laptop key\n")

    def test_a_key_that_is_commented_out_is_still_hidden(self):
        text = f"[Interface]\n# PrivateKey = {PRIVATE_KEY}\n#PresharedKey={PRESHARED_KEY}\n"
        mask = wireguard(text)
        self.assertEqual(mask.display_text, "[Interface]\n# PrivateKey = ‹secret 1›\n#PresharedKey=‹secret 2›\n")
        self.assertEqual(restored(mask, mask.display_text), text)

    def test_a_key_without_a_value_has_nothing_to_hide(self):
        text = "[Interface]\nPrivateKey =\nPrivateKey = # nothing\nPrivateKey\n"
        self.assertEqual(wireguard(text).display_text, text)

    def test_other_keys_and_their_values_are_left_alone(self):
        text = "[Interface]\nPrivateKeyFile = /etc/key\nMyPrivateKey = x\nAddress = 10.0.0.2/32 # PrivateKey = x\n"
        self.assertEqual(wireguard(text).display_text, text)

    def test_keeps_crlf_line_endings(self):
        text = WIREGUARD_PROFILE.replace("\n", "\r\n")
        mask = wireguard(text)
        self.assertIn("PrivateKey = ‹secret 1›\r\n", mask.display_text)
        self.assertNotIn(PRIVATE_KEY, mask.display_text)
        self.assertEqual(restored(mask, mask.display_text), text)

    def test_keeps_a_byte_order_mark_and_text_in_other_scripts(self):
        text = f"﻿# 備註 ‹ 🌐\n[Interface]\nPrivateKey = {PRIVATE_KEY} # 筆電\n"
        mask = wireguard(text)
        self.assertEqual(mask.display_text, "﻿# 備註 ‹ 🌐\n[Interface]\nPrivateKey = ‹secret 1› # 筆電\n")
        self.assertEqual(restored(mask, mask.display_text), text)

    def test_a_key_on_the_first_line_is_hidden_behind_a_byte_order_mark(self):
        text = f"﻿PrivateKey = {PRIVATE_KEY}\n"
        mask = wireguard(text)
        self.assertEqual(mask.display_text, "﻿PrivateKey = ‹secret 1›\n")
        self.assertEqual(restored(mask, mask.display_text), text)

    def test_a_private_key_in_a_wireguard_file_is_hidden_too(self):
        text = "[Interface]\n-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"
        mask = wireguard(text)
        self.assertEqual(mask.display_text, "[Interface]\n‹secret 1›\n")
        self.assertEqual(restored(mask, mask.display_text), text)


class OpenVPNMaskTests(unittest.TestCase):
    def test_hides_the_key_blocks_of_the_router_profile(self):
        text = repository_text("internal/ovpn/testdata/merlin.ovpn")
        mask = openvpn(text)
        lines = mask.display_text.split("\n")
        # The certificates are public and stay; the private key and the tls-crypt key do not.
        self.assertIn("<ca>", lines)
        self.assertIn("bWVybGluIGNh", lines)
        self.assertIn("bWVybGluIGNlcnQ=", lines)
        self.assertNotIn("bWVybGluIGtleQ==", lines)
        self.assertNotIn("ffeeddccbbaa99887766554433221100", lines)
        self.assertIn("<key>\n‹secret 1›\n</key>", mask.display_text)
        self.assertIn("<tls-crypt>\n‹secret 2›\n</tls-crypt>", mask.display_text)
        self.assertEqual(restored(mask, mask.display_text), text)

    def test_hides_the_key_blocks_of_a_profile_with_crlf(self):
        text = repository_text("internal/ovpn/testdata/windows.ovpn")
        self.assertIn("\r\n", text)
        mask = openvpn(text)
        self.assertIn("<key>\r\n‹secret 1›\r\n</key>\r\n", mask.display_text)
        self.assertIn("<tls-auth>\r\n‹secret 2›\r\n</tls-auth>", mask.display_text)
        self.assertNotIn("ZmFrZSBrZXk=", mask.display_text)
        self.assertEqual(restored(mask, mask.display_text), text)

    def test_a_secret_of_several_lines_comes_back_with_its_own_line_breaks(self):
        body = "-----BEGIN PRIVATE KEY-----\r\nAAAA\r\nBBBB\r\n-----END PRIVATE KEY-----"
        text = f"client\r\n<key>\r\n{body}\r\n</key>\r\nverb 3\r\n"
        mask = openvpn(text)
        self.assertEqual(mask.display_text, "client\r\n<key>\r\n‹secret 1›\r\n</key>\r\nverb 3\r\n")
        self.assertEqual(restored(mask, mask.display_text), text)

    def test_leaves_the_other_blocks_and_the_directives_alone(self):
        for name in ["asus", "connection-blocks"]:
            with self.subTest(name=name):
                text = repository_text(f"internal/ovpn/testdata/{name}.ovpn")
                mask = openvpn(text)
                hidden = "<key>" in text or "<tls-" in text
                self.assertEqual(mask.display_text != text, hidden)
                self.assertEqual(restored(mask, mask.display_text), text)
                self.assertTrue("<ca>\n-----BEGIN CERTIFICATE-----" in mask.display_text or "<ca>" not in text)

    def test_hides_every_secret_block(self):
        for tag in ["tls-auth", "tls-crypt", "tls-crypt-v2", "pkcs12", "secret", "auth-user-pass", "http-proxy-user-pass", "key"]:
            with self.subTest(tag=tag):
                text = f"client\n<{tag}>\nline one\nline two\n</{tag}>\nverb 3\n"
                mask = openvpn(text)
                self.assertEqual(mask.display_text, f"client\n<{tag}>\n‹secret 1›\n</{tag}>\nverb 3\n")
                self.assertEqual(restored(mask, mask.display_text), text)

    def test_shows_the_blocks_that_hold_nothing_secret(self):
        for tag in ["ca", "cert", "dh", "crl-verify", "extra-certs", "peer-fingerprint", "connection"]:
            with self.subTest(tag=tag):
                text = f"client\n<{tag}>\nline one\nline two\n</{tag}>\nverb 3\n"
                self.assertEqual(openvpn(text).display_text, text)

    def test_finds_a_block_whatever_the_spacing_around_its_tags(self):
        text = "client\n  <key>  \t\n  body line  \n\t</key>   \n<TLS-CRYPT> # the static key\nkey data\n</tls-crypt>\n"
        mask = openvpn(text)
        self.assertEqual(
            mask.display_text,
            "client\n  <key>  \t\n‹secret 1›\n\t</key>   \n<TLS-CRYPT> # the static key\n‹secret 2›\n</tls-crypt>\n",
        )
        self.assertEqual(restored(mask, mask.display_text), text)

    def test_a_tag_in_a_comment_or_inside_another_block_opens_nothing(self):
        text = "client\n# <key>\n; <tls-auth>\n<ca>\n<key>\nnot a block, a line of the certificate\n</ca>\nverb 3\n"
        self.assertEqual(openvpn(text).display_text, text)

    def test_a_block_without_a_body_hides_nothing(self):
        text = "client\n<key>\n</key>\n<tls-auth>\n \t\n\n</tls-auth>\n"
        self.assertEqual(openvpn(text).display_text, text)

    def test_a_block_that_is_never_closed_runs_to_the_end(self):
        text = "client\n<key>\nSECRET LINE ONE\nSECRET LINE TWO\n"
        mask = openvpn(text)
        self.assertEqual(mask.display_text, "client\n<key>\n‹secret 1›\n")
        self.assertEqual(restored(mask, mask.display_text), text)

    def test_a_block_without_a_line_break_at_the_end_still_closes(self):
        text = "client\n<key>\nSECRET\n</key>"
        mask = openvpn(text)
        self.assertEqual(mask.display_text, "client\n<key>\n‹secret 1›\n</key>")
        self.assertEqual(restored(mask, mask.display_text), text)


class PemOutsideBlocksTests(unittest.TestCase):
    def test_a_private_key_outside_any_block_is_hidden(self):
        key = "-----BEGIN PRIVATE KEY-----\nAAAA\nBBBB\n-----END PRIVATE KEY-----"
        text = f"client\n{key}\nverb 3\n"
        mask = openvpn(text)
        self.assertEqual(mask.display_text, "client\n‹secret 1›\nverb 3\n")
        self.assertEqual(restored(mask, mask.display_text), text)

    def test_hides_pem_text_outside_blocks_only_when_it_is_a_key(self):
        for label, secret in [
            ("ENCRYPTED PRIVATE KEY", True), ("RSA PRIVATE KEY", True), ("EC PRIVATE KEY", True),
            ("OpenVPN Static key V1", True), ("OpenVPN tls-crypt-v2 client key", True),
            ("CERTIFICATE", False), ("PUBLIC KEY", False), ("DH PARAMETERS", False),
        ]:
            with self.subTest(label=label):
                text = f"client\n  -----BEGIN {label}-----\n  AAAA\n  -----END {label}-----\n"
                self.assertEqual(openvpn(text).display_text != text, secret)

    def test_a_private_key_inside_another_block_is_hidden_without_hiding_the_block(self):
        text = (
            "client\n<cert>\n-----BEGIN CERTIFICATE-----\nCCCC\n-----END CERTIFICATE-----\n"
            "-----BEGIN PRIVATE KEY-----\nKKKK\n-----END PRIVATE KEY-----\n</cert>\n"
        )
        mask = openvpn(text)
        self.assertEqual(
            mask.display_text,
            "client\n<cert>\n-----BEGIN CERTIFICATE-----\nCCCC\n-----END CERTIFICATE-----\n‹secret 1›\n</cert>\n",
        )
        self.assertEqual(restored(mask, mask.display_text), text)

    def test_pem_text_without_its_end_line_is_not_a_key(self):
        text = "client\n-----BEGIN PRIVATE KEY-----\nAAAA\nverb 3\n"
        self.assertEqual(openvpn(text).display_text, text)


class PlaceholderTests(unittest.TestCase):
    def test_a_text_that_already_has_a_placeholder_gets_numbers_that_do_not_collide_with_it(self):
        text = f"# was ‹secret 1› and ‹secret 3›\n[Interface]\nPrivateKey = {PRIVATE_KEY}\nPresharedKey = {PRESHARED_KEY}\n"
        mask = wireguard(text)
        self.assertEqual(
            mask.display_text,
            "# was ‹secret 1› and ‹secret 3›\n[Interface]\nPrivateKey = ‹secret 2›\nPresharedKey = ‹secret 4›\n",
        )
        self.assertEqual(restored(mask, mask.display_text), text)

    def test_finds_placeholders_by_their_exact_shape(self):
        text = "a ‹secret 1› b ‹secret 12›‹secret 7› ‹secret 0› ‹secret 01› ‹secret › ‹secret x› ‹secret 5 ‹Secret 6› ‹secret 99999999999999999999›"
        found = [text[start:end] for start, end in placeholder_ranges(text)]
        self.assertEqual(found, ["‹secret 1›", "‹secret 12›", "‹secret 7›"])


class EditTests(unittest.TestCase):
    OVPN_TEXT = "client\nremote vpn.example.net 1194\n<key>\nKEY BODY\n</key>\n<tls-crypt>\nCRYPT BODY\n</tls-crypt>\nverb 3\n"

    def test_text_typed_next_to_a_placeholder_does_not_touch_the_secret(self):
        mask = wireguard(WIREGUARD_PROFILE)
        edited = mask.display_text.replace("PrivateKey = ‹secret 1›", "PrivateKey = ‹secret 1›abc").replace(
            "PresharedKey = ‹secret 2›", "PresharedKey = xyz‹secret 2›"
        )
        self.assertEqual(
            restored(mask, edited),
            WIREGUARD_PROFILE.replace(f"PrivateKey = {PRIVATE_KEY}", f"PrivateKey = {PRIVATE_KEY}abc").replace(
                f"PresharedKey = {PRESHARED_KEY}", f"PresharedKey = xyz{PRESHARED_KEY}"
            ),
        )

    def test_a_placeholder_that_is_deleted_takes_its_secret_with_it(self):
        mask = openvpn(self.OVPN_TEXT)
        result = restored(mask, mask.display_text.replace("‹secret 1›\n", ""))
        self.assertEqual(result, "client\nremote vpn.example.net 1194\n<key>\n</key>\n<tls-crypt>\nCRYPT BODY\n</tls-crypt>\nverb 3\n")
        self.assertNotIn("KEY BODY", result)

    def test_text_typed_over_a_placeholder_replaces_the_secret(self):
        mask = wireguard(WIREGUARD_PROFILE)
        result = restored(mask, mask.display_text.replace("‹secret 1›", "NEW-PRIVATE-KEY"))
        self.assertIn("PrivateKey = NEW-PRIVATE-KEY\n", result)
        self.assertNotIn(PRIVATE_KEY, result)
        self.assertIn(f"PresharedKey = {PRESHARED_KEY}\n", result)

    def test_a_damaged_placeholder_is_text_and_its_secret_is_gone(self):
        for damaged in ["‹secret 1", "secret 1›", "‹secret ›", "‹secret1›", "‹Secret 1›", "‹secret 1 ›", "secret 1", "‹secret 01›"]:
            with self.subTest(damaged=damaged):
                mask = wireguard(f"[Interface]\nPrivateKey = {PRIVATE_KEY}\n")
                edited = mask.display_text.replace("‹secret 1›", damaged)
                self.assertEqual(restored(mask, edited), f"[Interface]\nPrivateKey = {damaged}\n")

    def test_a_placeholder_of_an_unknown_number_stays_as_it_is(self):
        mask = wireguard(f"[Interface]\nPrivateKey = {PRIVATE_KEY}\n")
        edited = mask.display_text + "# ‹secret 2› ‹secret 9›\n"
        self.assertEqual(restored(mask, edited), f"[Interface]\nPrivateKey = {PRIVATE_KEY}\n# ‹secret 2› ‹secret 9›\n")

    def test_a_placeholder_that_is_copied_is_an_error_not_a_guess(self):
        mask = openvpn(self.OVPN_TEXT)
        with self.assertRaises(DuplicatedPlaceholder) as raised:
            mask.restore(mask.display_text + "# ‹secret 2›\n")
        self.assertEqual((raised.exception.number, raised.exception.line), (2, 10))

    def test_a_moved_placeholder_carries_its_secret_along(self):
        mask = openvpn(self.OVPN_TEXT)
        # Cut the first block's placeholder and paste it into the second block, and the other way round.
        edited = mask.display_text.replace("<key>\n‹secret 1›\n", "<key>\n‹secret 2›\n").replace(
            "<tls-crypt>\n‹secret 2›\n", "<tls-crypt>\n‹secret 1›\n"
        )
        expected = self.OVPN_TEXT.replace("KEY BODY", "TMP").replace("CRYPT BODY", "KEY BODY").replace("TMP", "CRYPT BODY")
        self.assertEqual(restored(mask, edited), expected)

    def test_a_secret_comes_back_as_it_was_even_when_it_looks_like_a_placeholder(self):
        text = "client\n<key>\n‹secret 1›\n‹secret 2›\n</key>\n"
        mask = openvpn(text)
        self.assertEqual(mask.display_text, "client\n<key>\n‹secret 3›\n</key>\n")
        self.assertEqual(restored(mask, mask.display_text), text)

    def test_the_text_of_a_display_without_secrets_is_returned_as_it_is(self):
        mask = openvpn("client\nremote vpn.example.net 1194\n")
        self.assertEqual(mask.display_text, "client\nremote vpn.example.net 1194\n")
        self.assertEqual(restored(mask, "client\nremote other 443\n\n"), "client\nremote other 443\n\n")


class LineMapTests(unittest.TestCase):
    def test_maps_a_line_of_the_restored_text_back_to_the_line_of_the_display(self):
        text = "client\n<key>\nA\nB\nC\n</key>\nverb 3\n<tls-auth>\nD\nE\n</tls-auth>\nremote x 1\n"
        mask = openvpn(text)
        self.assertEqual(
            mask.display_text,
            "client\n<key>\n‹secret 1›\n</key>\nverb 3\n<tls-auth>\n‹secret 2›\n</tls-auth>\nremote x 1\n",
        )
        restoration = mask.restore(mask.display_text)
        self.assertEqual(restoration.text, text)

        # Every line that is not part of a secret is found again in the display.
        secret_lines = {"A", "B", "C", "D", "E"}
        restored_lines = text.split("\n")
        display_lines = mask.display_text.split("\n")
        for index, line in enumerate(restored_lines):
            if line and line not in secret_lines:
                shown = restoration.display_line(index + 1)
                self.assertEqual(display_lines[shown - 1], line, f"restored line {index + 1} is {line}")
        # Lines inside a secret map to the line of its placeholder.
        for line, shown in [(3, 3), (4, 3), (5, 3), (9, 7), (10, 7)]:
            self.assertEqual(restoration.display_line(line), shown, f"line {line}")

    def test_lines_before_the_first_secret_and_after_the_last_are_counted_as_they_are(self):
        mask = wireguard(f"[Interface]\nPrivateKey = {PRIVATE_KEY}\nAddress = 10.0.0.2/32\n")
        restoration = mask.restore(mask.display_text)
        self.assertEqual([restoration.display_line(line) for line in (1, 2, 3)], [1, 2, 3])

    def test_the_line_map_follows_the_edited_text_not_the_original_one(self):
        mask = openvpn("client\n<key>\nA\nB\n</key>\nverb 3\n")
        # Two lines added above the placeholder; the secret then spans restored lines 5 and 6.
        restoration = mask.restore("client\n# one\n# two\n<key>\n‹secret 1›\n</key>\nverb 3\n")
        self.assertEqual(restoration.text, "client\n# one\n# two\n<key>\nA\nB\n</key>\nverb 3\n")
        self.assertEqual([restoration.display_line(line) for line in (1, 4, 5, 6, 7, 8)], [1, 4, 5, 5, 6, 7])


# MARK: Random edits

ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
INSERTIONS = ["x", " ", "# note", "\n", "\r\n", "Address = 10.0.0.9/32", "備註", "🌐", "9", "<key>", "</key>", "PrivateKey = "]


def random_profile(seed: int):
    """A profile built to hold known secrets: (text, kind, {placeholder number: secret})."""
    rng = random.Random(seed)
    line_break = rng.choice(["\n", "\r\n"])

    def base64(count: int) -> str:
        return "".join(rng.choice(ALPHABET) for _ in range(count))

    def blanks() -> str:
        return rng.choice([" ", "\t", ""]) * rng.randint(0, 3)

    lines: list[str] = []
    secrets: dict[int, str] = {}

    def add_secret(secret: str) -> None:
        secrets[len(secrets) + 1] = secret

    if rng.random() < 0.5:
        kind = ProfileKind.WIREGUARD
        key = base64(43) + "="
        lines += ["[Interface]", f"PrivateKey = {key}"]
        add_secret(key)
        for index in range(rng.randint(1, 6)):
            match rng.randrange(5):
                case 0:
                    key = base64(43) + "="
                    lines.append(f"{blanks()}PrivateKey{blanks()}={blanks()}{key}{blanks()}")
                    add_secret(key)
                case 1:
                    key = base64(43) + "="
                    lines.append(f"{blanks()}PresharedKey = {key} # note {index}")
                    add_secret(key)
                case 2:
                    lines.append(f"# a comment {base64(8)} ‹ › 備註")
                case 3:
                    lines.append("")
                case _:
                    lines.append(f"Address = 10.{index}.0.2/32")
        lines += ["[Peer]", f"PublicKey = {base64(43)}="]
    else:
        kind = ProfileKind.OPENVPN
        key = base64(40)
        lines += ["client", "<key>", key, "</key>"]
        add_secret(key)
        for index in range(rng.randint(1, 6)):
            match rng.randrange(4):
                case 0:
                    body = line_break.join(base64(32) for _ in range(rng.randint(1, 4)))
                    tag = rng.choice(["key", "tls-crypt", "tls-auth", "secret"])
                    lines += [f"{blanks()}<{tag}>{blanks()}", body, f"{blanks()}</{tag}>{blanks()}"]
                    add_secret(body)
                case 1:
                    lines += ["<ca>", "-----BEGIN CERTIFICATE-----", base64(32), "-----END CERTIFICATE-----", "</ca>"]
                case 2:
                    lines.append(f"; comment {base64(6)} ‹ ›")
                case _:
                    lines.append(f"remote host{index}.example.net 1194")
    return line_break.join(lines) + line_break, kind, secrets


def placeholder_number(text: str, span) -> int:
    return int(text[span[0] + len("‹secret ") : span[1] - 1])


def edit_away_from_placeholders(text: str, rng: random.Random) -> str:
    """One insertion, deletion or replacement that leaves every placeholder whole."""
    gaps = []
    start = 0
    for begin, end in placeholder_ranges(text):
        gaps.append((start, begin))
        start = end
    gaps.append((start, len(text)))
    gap_start, gap_end = rng.choice(gaps)
    first = rng.randint(gap_start, gap_end)
    last = first + rng.randint(0, min(6, gap_end - first))
    return text[:first] + rng.choice(INSERTIONS) + text[last:]


class RandomEditTests(unittest.TestCase):
    def test_edits_away_from_placeholders_keep_every_secret_byte_identical(self):
        for seed in range(150):
            with self.subTest(seed=seed):
                text, kind, secrets = random_profile(seed)
                mask = SecretMask(text, kind)

                # The mask finds exactly what the profile was built to hold, and hides all of it.
                numbers = [placeholder_number(mask.display_text, span) for span in placeholder_ranges(mask.display_text)]
                self.assertEqual(numbers, sorted(secrets))
                for secret in secrets.values():
                    self.assertNotIn(secret, mask.display_text)
                self.assertEqual(mask.restore(mask.display_text).text, text)

                rng = random.Random(seed + 1_000_003)
                edited = mask.display_text
                for _ in range(rng.randint(1, 8)):
                    edited = edit_away_from_placeholders(edited, rng)

                # What `edited` has to become: its placeholders replaced by the secrets.
                expected = edited
                for span in reversed(placeholder_ranges(edited)):
                    expected = expected[: span[0]] + secrets[placeholder_number(edited, span)] + expected[span[1] :]
                result = mask.restore(edited).text
                self.assertEqual(result, expected)
                for secret in secrets.values():
                    self.assertIn(secret, result)


class CostTests(unittest.TestCase):
    def test_many_begin_lines_without_an_end_are_scanned_once(self):
        # A hostile profile is accepted by the daemon with any body in an allowed block: many
        # BEGIN lines and no END line must not be searched to the end of the text once for each.
        lines = "-----BEGIN PRIVATE KEY-----\n" * 30_000
        text = "client\n<ca>\n" + lines + "</ca>\n"
        start = time.monotonic()
        mask = openvpn(text)
        elapsed = time.monotonic() - start
        self.assertEqual(mask.display_text, text, "no key was found: nothing is hidden")
        self.assertLess(elapsed, 3, f"took {elapsed:.1f} s")

    def test_a_key_after_many_unfinished_ones_of_another_label_is_still_hidden(self):
        unfinished = "-----BEGIN RSA PRIVATE KEY-----\n" * 50
        text = "<ca>\n" + unfinished + "-----BEGIN PRIVATE KEY-----\nSECRETBODY\n-----END PRIVATE KEY-----\n</ca>\n"
        mask = openvpn(text)
        self.assertNotIn("SECRETBODY", mask.display_text)
        self.assertEqual(restored(mask, mask.display_text), text)


if __name__ == "__main__":
    unittest.main()
