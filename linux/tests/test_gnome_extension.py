"""The GNOME Shell extension (data/share/gnome-shell/extensions): its metadata, and the logic of its top bar
item, which gjs runs. The part that needs the Shell is not run here."""

import json
import shutil
import subprocess
import unittest
from pathlib import Path

LINUX_DIR = Path(__file__).resolve().parent.parent
EXTENSION = LINUX_DIR / "data/share/gnome-shell/extensions/plaitway@koukeneko.github.io"


class GnomeExtensionTests(unittest.TestCase):
    def test_the_metadata_names_the_folder_it_is_in(self):
        metadata = json.loads((EXTENSION / "metadata.json").read_text(encoding="utf-8"))
        self.assertEqual(metadata["uuid"], EXTENSION.name)
        self.assertTrue((EXTENSION / "extension.js").is_file())
        self.assertTrue(metadata["shell-version"])

    @unittest.skipUnless(shutil.which("gjs"), "gjs is not installed")
    def test_the_status_logic_passes_under_gjs(self):
        result = subprocess.run(
            ["gjs", "-m", str(LINUX_DIR / "tests/gnome_status_test.js")], capture_output=True, text=True, timeout=60
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("ok", result.stdout)


if __name__ == "__main__":
    unittest.main()
