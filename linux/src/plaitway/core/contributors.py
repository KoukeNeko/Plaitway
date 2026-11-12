"""The people who contributed to the project, for the About section of the Settings page.

A package carries the list the build made (`contributors.json`, written by tools/gen_contributors.py
in the CI, which asks GitHub once). A checkout has no such file and asks GitHub itself each time the
page is opened; an installed app never goes to the network for it.
"""

from __future__ import annotations

import json
import urllib.request
from dataclasses import dataclass
from pathlib import Path

REPOSITORY = "KoukeNeko/Plaitway"
REPOSITORY_URL = f"https://github.com/{REPOSITORY}"
API_URL = f"https://api.github.com/repos/{REPOSITORY}/contributors?per_page=100"
# Next to the `plaitway` package's own modules, so that the package ships it.
BAKED_FILE = Path(__file__).resolve().parents[1] / "contributors.json"
TIMEOUT_SECONDS = 10


@dataclass(frozen=True)
class Contributor:
    login: str
    url: str
    contributions: int


def parse(answer: object) -> list[Contributor]:
    """The contributors of GitHub's answer, most active first; bots are not people."""
    if not isinstance(answer, list):
        raise ValueError("GitHub did not answer with a list of contributors")
    people = [
        Contributor(item["login"], item["html_url"], int(item.get("contributions", 0)))
        for item in answer
        if isinstance(item, dict) and item.get("type") != "Bot" and "login" in item and "html_url" in item
    ]
    return sorted(people, key=lambda person: -person.contributions)


def fetch(token: str | None = None) -> list[Contributor]:
    """Asks GitHub. Blocking: not for the UI thread."""
    headers = {"Accept": "application/vnd.github+json", "User-Agent": "plaitway-about"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    request = urllib.request.Request(API_URL, headers=headers)
    with urllib.request.urlopen(request, timeout=TIMEOUT_SECONDS) as response:
        return parse(json.load(response))


def baked(path: Path = BAKED_FILE) -> list[Contributor]:
    """The list the build wrote; empty when there is none or it does not make sense."""
    try:
        stored = json.loads(path.read_text(encoding="utf-8"))
        return [Contributor(item["login"], item["url"], int(item["contributions"])) for item in stored]
    except (OSError, ValueError, KeyError, TypeError):
        return []


def save(people: list[Contributor], path: Path = BAKED_FILE) -> None:
    stored = [{"login": p.login, "url": p.url, "contributions": p.contributions} for p in people]
    path.write_text(json.dumps(stored, indent=2) + "\n", encoding="utf-8")
