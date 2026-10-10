"""The helper service of a system that does not run systemd."""

import tempfile
import unittest
from pathlib import Path

from plaitway.app.systemd import SystemdHelperService
from plaitway.core.ports import HelperRegistration, HelperServiceError


class WithoutSystemdTests(unittest.TestCase):
    """The directory systemd makes when it is the init is not there: no question goes to a bus."""

    def setUp(self):
        self.service = SystemdHelperService(run_dir=Path(tempfile.gettempdir()) / "no-such-run-systemd-system")

    def test_the_registration_says_there_is_no_systemd(self):
        self.assertIs(self.service.registration(), HelperRegistration.NO_SYSTEMD)

    def test_starting_and_restarting_say_why_they_cannot(self):
        for call in (self.service.start, self.service.restart):
            with self.subTest(call=call.__name__), self.assertRaisesRegex(HelperServiceError, "does not run systemd"):
                call()


if __name__ == "__main__":
    unittest.main()
