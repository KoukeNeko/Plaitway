import os
import shutil
import tempfile
import time
import unittest

from plaitway.client import profile_importer as importer
from plaitway.client.types import Credentials

CERTIFICATE = "-----BEGIN CERTIFICATE-----\nMIIBtest\n-----END CERTIFICATE-----"
PRIVATE_KEY = "-----BEGIN PRIVATE KEY-----\nMIIEtest\n-----END PRIVATE KEY-----"
STATIC_KEY = "#\n# 2048 bit OpenVPN static key\n#\n-----BEGIN OpenVPN Static key V1-----\n0123\n-----END OpenVPN Static key V1-----"


class ImporterTestCase(unittest.TestCase):
    def directory(self, files: dict[str, str] | None = None) -> str:
        """A directory with the given files; removed when the test is over."""
        root = tempfile.mkdtemp(prefix="pw-import-")
        self.addCleanup(shutil.rmtree, root, True)
        for name, content in (files or {}).items():
            path = os.path.join(root, name)
            os.makedirs(os.path.dirname(path), exist_ok=True)
            with open(path, "wb") as file:
                file.write(content.encode())
        return root

    def inline(self, profile: str, files: dict[str, str] | None = None) -> str:
        return importer.inline_referenced_files(profile, self.directory(files))[0]

    def profile_next_to_a_secret(self) -> tuple[str, str]:
        """`profile/` next to `outside.pem`, which a hostile profile would like to read."""
        root = self.directory({"profile/ca.crt": CERTIFICATE, "outside.pem": PRIVATE_KEY})
        return os.path.join(root, "profile"), root

    def assert_refused_outside(self, profile: str, directory: str, path: str) -> None:
        with self.assertRaises(importer.OutsideProfileDirectory) as raised:
            importer.inline_referenced_files(profile, directory)
        self.assertEqual(raised.exception.path, path)


class InliningTests(ImporterTestCase):
    def test_stops_inlining_as_soon_as_the_result_is_too_large(self):
        # A profile is untrusted: the same file named on every line of 1 MiB would be read and
        # copied for each, and the size was only checked when the result was done.
        large = "-----BEGIN CERTIFICATE-----\n" + "A" * 200_000 + "\n-----END CERTIFICATE-----\n"
        profile = "client\n" + "ca big.pem\n" * 100_000
        directory = self.directory({"big.pem": large})
        start = time.monotonic()
        with self.assertRaises(importer.TooLarge):
            importer.inline_referenced_files(profile, directory)
        self.assertLess(time.monotonic() - start, 5)

    def test_inlines_ca_cert_and_key_files(self):
        result = self.inline(
            "client\nremote vpn.example.net 1194\nca ca.crt\ncert client.crt\nkey keys/client.key\nverb 3\n",
            {"ca.crt": CERTIFICATE + "\n", "client.crt": CERTIFICATE, "keys/client.key": PRIVATE_KEY + "\n"},
        )
        self.assertEqual(
            result,
            f"client\nremote vpn.example.net 1194\n<ca>\n{CERTIFICATE}\n</ca>\n<cert>\n{CERTIFICATE}\n</cert>\n"
            f"<key>\n{PRIVATE_KEY}\n</key>\nverb 3\n",
        )

    def test_inlines_tls_auth_with_its_key_direction(self):
        result = self.inline("client\ntls-auth ta.key 1\n", {"ta.key": STATIC_KEY})
        self.assertEqual(result, f"client\n<tls-auth>\n{STATIC_KEY}\n</tls-auth>\nkey-direction 1\n")

    def test_does_not_repeat_a_key_direction_the_profile_already_has(self):
        result = self.inline("key-direction 1\ntls-auth ta.key 1\n", {"ta.key": STATIC_KEY})
        self.assertEqual(result.count("key-direction"), 1)

    def test_inlines_tls_crypt(self):
        result = self.inline("tls-crypt tc.key\n", {"tc.key": STATIC_KEY})
        self.assertEqual(result, f"<tls-crypt>\n{STATIC_KEY}\n</tls-crypt>\n")

    def test_leaves_inline_blocks_and_complex_lines_alone(self):
        profile = f"""client
# ca ignored.crt
; key ignored.key
dh none
ca [inline]
<ca>
ca not-a-directive-inside-a-block.crt
{CERTIFICATE}
</ca>
auth-user-pass
remote-cert-tls server
"""
        self.assertEqual(self.inline(profile), profile)

    def test_resolves_relative_paths_against_the_profiles_directory(self):
        root = self.directory({"profiles/ca.crt": CERTIFICATE, "profiles/certs/key.pem": PRIVATE_KEY})
        result, _ = importer.inline_referenced_files(
            'ca ca.crt\nkey "certs/key.pem"\nca certs/../ca.crt\n', os.path.join(root, "profiles")
        )
        self.assertEqual(
            result, f"<ca>\n{CERTIFICATE}\n</ca>\n<key>\n{PRIVATE_KEY}\n</key>\n<ca>\n{CERTIFICATE}\n</ca>\n"
        )

    def test_understands_windows_line_endings(self):
        result = self.inline(
            f"client\r\nca ca.crt\r\n<cert>\r\n{CERTIFICATE}\r\n</cert>\r\nverb 3\r\n", {"ca.crt": CERTIFICATE}
        )
        self.assertNotIn("\r", result)
        self.assertIn(f"<ca>\n{CERTIFICATE}\n</ca>", result)
        self.assertIn("</cert>\nverb 3", result)

    def test_reads_paths_the_way_openvpn_does(self):
        for line in [
            'ca "my certs/ca file.crt"',
            "ca 'my certs/ca file.crt'",
            "ca my\\ certs/ca\\ file.crt",
            '  CA   "my certs/ca file.crt"  ',
        ]:
            with self.subTest(line=line):
                result = self.inline(line + "\n", {"my certs/ca file.crt": CERTIFICATE})
                self.assertEqual(result, f"<ca>\n{CERTIFICATE}\n</ca>\n")


class OutsideTheProfileDirectoryTests(ImporterTestCase):
    def test_refuses_a_path_that_leaves_the_profiles_directory(self):
        for path in ["../outside.pem", "certs/../../outside.pem"]:
            with self.subTest(path=path):
                profile_directory, _ = self.profile_next_to_a_secret()
                self.assert_refused_outside(f"key {path}\n", profile_directory, path)

    def test_refuses_an_absolute_path_even_inside_the_profiles_directory(self):
        profile_directory, root = self.profile_next_to_a_secret()
        for path in [os.path.join(root, "outside.pem"), os.path.join(profile_directory, "ca.crt")]:
            with self.subTest(path=path):
                self.assert_refused_outside(f"ca {path}\n", profile_directory, path)

    def test_refuses_a_home_directory_path(self):
        profile_directory, _ = self.profile_next_to_a_secret()
        self.assert_refused_outside("key ~/.ssh/id_ed25519\n", profile_directory, "~/.ssh/id_ed25519")

    def test_refuses_a_symlink_that_points_outside_the_profiles_directory(self):
        profile_directory, root = self.profile_next_to_a_secret()
        os.symlink(os.path.join(root, "outside.pem"), os.path.join(profile_directory, "link.pem"))
        os.symlink(root, os.path.join(profile_directory, "linked"))
        self.assert_refused_outside("key link.pem\n", profile_directory, "link.pem")
        self.assert_refused_outside("key linked/outside.pem\n", profile_directory, "linked/outside.pem")

    def test_a_profile_directory_reached_through_a_symlink_is_still_the_profiles_directory(self):
        profile_directory, root = self.profile_next_to_a_secret()
        alias = os.path.join(root, "alias")
        os.symlink(profile_directory, alias)
        result, _ = importer.inline_referenced_files("ca ca.crt\n", alias)
        self.assertEqual(result, f"<ca>\n{CERTIFICATE}\n</ca>\n")

    def test_follows_a_symlink_to_a_regular_file(self):
        directory = self.directory({"real/ca.crt": CERTIFICATE})
        os.symlink(os.path.join(directory, "real/ca.crt"), os.path.join(directory, "link.crt"))
        result, _ = importer.inline_referenced_files("ca link.crt\n", directory)
        self.assertEqual(result, f"<ca>\n{CERTIFICATE}\n</ca>\n")

    def test_refuses_an_auth_user_pass_file_outside_the_profiles_directory(self):
        for path in ["../outside.pem", "/etc/hosts", "~/creds.txt"]:
            with self.subTest(path=path):
                profile_directory, _ = self.profile_next_to_a_secret()
                with self.assertRaises(importer.OutsideProfileDirectory) as raised:
                    importer.inline_referenced_files(f"auth-user-pass {path}\n", profile_directory)
                self.assertEqual((raised.exception.directive, raised.exception.path), ("auth-user-pass", path))


class RefusedFilesTests(ImporterTestCase):
    def test_names_the_missing_file(self):
        directory = self.directory()
        with self.assertRaises(importer.MissingFile) as raised:
            importer.inline_referenced_files("client\nca nowhere.crt\n", directory)
        self.assertEqual(raised.exception.directive, "ca")
        self.assertTrue(raised.exception.path.endswith("/nowhere.crt"))

    def test_refuses_a_file_that_is_not_key_material(self):
        directory = self.directory({"notes.txt": "just some text\n"})
        with self.assertRaises(importer.NotKeyMaterial) as raised:
            importer.inline_referenced_files("key notes.txt\n", directory)
        self.assertEqual(raised.exception.directive, "key")

    def test_refuses_a_binary_file(self):
        directory = self.directory()
        with open(os.path.join(directory, "cert.der"), "wb") as file:
            file.write(bytes([0x30, 0x82, 0x00, 0xFF, 0xFE]))
        with self.assertRaises(importer.NotText):
            importer.inline_referenced_files("cert cert.der\n", directory)

    def test_refuses_to_read_what_is_not_a_regular_file(self):
        # A hostile profile can name a pipe or a directory next to it; reading a pipe never ends.
        directory = self.directory({"sub/placeholder": ""})
        os.mkfifo(os.path.join(directory, "pipe.pem"), 0o600)
        for name in ["pipe.pem", "sub"]:
            with self.subTest(name=name):
                with self.assertRaises(importer.NotText) as raised:
                    importer.inline_referenced_files(f"ca {name}\n", directory)
                self.assertTrue(raised.exception.path.endswith(name))


class AuthUserPassTests(ImporterTestCase):
    def test_replaces_a_file_by_its_bare_form_and_returns_what_it_holds(self):
        directory = self.directory({
            "office.ovpn": "client\nremote vpn.example.net 1194\nauth-user-pass creds.txt\nverb 3\n",
            "creds.txt": "alice\ns3cret\n",
        })
        loaded = importer.load(os.path.join(directory, "office.ovpn"))
        self.assertEqual(loaded.content.decode(), "client\nremote vpn.example.net 1194\nauth-user-pass\nverb 3\n")
        self.assertEqual(loaded.credentials, Credentials(username="alice", password="s3cret"))

    def test_reads_credentials_the_way_openvpn_does(self):
        # CR LF, trailing lines and spaces inside the password are kept or dropped as openvpn does.
        directory = self.directory({"pw.txt": "bob\r\npass word \r\nignored\r\n"})
        text, credentials = importer.inline_referenced_files('AUTH-USER-PASS "pw.txt"\n', directory)
        self.assertEqual(text, "auth-user-pass\n")
        self.assertEqual(credentials, Credentials(username="bob", password="pass word "))

    def test_leaves_a_bare_auth_user_pass_and_its_inline_block_alone(self):
        bare = "client\nauth-user-pass\n"
        self.assertEqual(self.inline(bare), bare)
        block = "<auth-user-pass>\nalice\ns3cret\n</auth-user-pass>\n"
        self.assertEqual(self.inline(block), block)
        self.assertIsNone(importer.inline_referenced_files(bare, self.directory())[1])

    def test_refuses_an_auth_user_pass_file_without_both_lines(self):
        for content in ["alice\n", "alice", "\ns3cret\n", "alice\n\n", ""]:
            with self.subTest(content=content):
                directory = self.directory({"creds.txt": content})
                with self.assertRaises(importer.NotCredentials) as raised:
                    importer.inline_referenced_files("auth-user-pass creds.txt\n", directory)
                self.assertTrue(raised.exception.path.endswith("/creds.txt"))

    def test_names_an_auth_user_pass_file_that_is_missing(self):
        with self.assertRaises(importer.MissingFile) as raised:
            importer.inline_referenced_files("auth-user-pass nowhere.txt\n", self.directory())
        self.assertEqual(raised.exception.directive, "auth-user-pass")
        self.assertTrue(raised.exception.path.endswith("/nowhere.txt"))


class LoadTests(ImporterTestCase):
    def test_loads_an_ovpn_file_with_its_referenced_files(self):
        directory = self.directory({
            "office.ovpn": "client\nremote vpn.example.net 1194\nca ca.crt\n",
            "ca.crt": CERTIFICATE,
        })
        data = importer.load(os.path.join(directory, "office.ovpn")).content
        self.assertEqual(data.decode(), f"client\nremote vpn.example.net 1194\n<ca>\n{CERTIFICATE}\n</ca>\n")

    def test_passes_a_wireguard_file_through_unchanged(self):
        # A line that would be a file reference in an OpenVPN profile.
        text = "[Interface]\nPrivateKey = AAAA\nAddress = 10.0.0.2/32\n\n[Peer]\nPublicKey = BBBB\nAllowedIPs = 0.0.0.0/0\nEndpoint = 203.0.113.5:51820\n"
        directory = self.directory({"home.conf": text})
        self.assertEqual(importer.load(os.path.join(directory, "home.conf")).content.decode(), text)

    def test_recognises_an_openvpn_profile_without_the_ovpn_extension(self):
        directory = self.directory({"router.conf": "client\nremote vpn.example.net\nca ca.crt\n", "ca.crt": CERTIFICATE})
        self.assertIn("<ca>", importer.load(os.path.join(directory, "router.conf")).content.decode())

    def test_refuses_a_profile_that_is_too_large(self):
        directory = self.directory({"huge.ovpn": "# padding\n" * 120_000})
        with self.assertRaises(importer.TooLarge):
            importer.load(os.path.join(directory, "huge.ovpn"))

    def test_reports_a_missing_profile(self):
        with self.assertRaises(importer.Unreadable) as raised:
            importer.load("/nonexistent/profile.ovpn")
        self.assertEqual(raised.exception.path, "/nonexistent/profile.ovpn")

    def test_keeps_a_byte_order_mark_of_a_wireguard_file(self):
        directory = self.directory()
        path = os.path.join(directory, "home.conf")
        with open(path, "wb") as file:
            file.write(b"\xef\xbb\xbf[Interface]\nPrivateKey = AAAA\n")
        self.assertTrue(importer.load(path).content.startswith(b"\xef\xbb\xbf[Interface]"))


if __name__ == "__main__":
    unittest.main()
