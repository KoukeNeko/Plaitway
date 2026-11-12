#!/usr/bin/env python3
"""Writes src/plaitway/contributors.json, the contributors that the package carries, from GitHub.

The CI runs it before it builds the package (GITHUB_TOKEN raises the rate limit). The file is not in
the repository. A failure is a warning, not an error: a package with no list shows no contributors.

  python tools/gen_contributors.py
"""

import os
import sys
import urllib.error
from pathlib import Path

LINUX_DIR = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(LINUX_DIR / "src"))

from plaitway.core import contributors  # noqa: E402


def main() -> None:
    try:
        people = contributors.fetch(os.environ.get("GITHUB_TOKEN"))
    except (urllib.error.URLError, ValueError, OSError) as error:
        print(f"::warning::the contributors were not fetched, the package has none: {error}")
        return
    contributors.save(people)
    print(f"wrote {contributors.BAKED_FILE} with {len(people)} contributors")


if __name__ == "__main__":
    main()
