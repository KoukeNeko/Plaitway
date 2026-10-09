"""Which language the UI is in: the desktop's, unless PLAITWAY_LANGUAGE says otherwise."""

from __future__ import annotations

import os
import re
from collections.abc import Mapping

from .strings import LANGUAGES

OVERRIDE_VARIABLE = "PLAITWAY_LANGUAGE"
# Where gettext looks, most specific first. LANGUAGE is a colon separated list.
LOCALE_VARIABLES = ("LC_ALL", "LC_MESSAGES", "LANG")

_TRADITIONAL_REGIONS = {"TW", "HK", "MO"}


def language_of(tag: str) -> str | None:
    """The language of the UI that a locale name asks for (`zh_TW.UTF-8`,
    `en-US`, `C`), or None when it is one the app does not have. Simplified
    Chinese is not Traditional Chinese."""
    name = re.split(r"[.@]", tag.strip(), maxsplit=1)[0]
    parts = [part for part in re.split(r"[_-]", name) if part]
    if not parts:
        return None
    language = parts[0].lower()
    if language in ("c", "posix", "en"):
        return "en"
    if language == "zh":
        rest = {part.upper() for part in parts[1:]}
        if "HANT" in rest or rest & _TRADITIONAL_REGIONS:
            return "zh-Hant"
    return None


def detect_language(environment: Mapping[str, str] | None = None) -> str:
    """PLAITWAY_LANGUAGE, else the first language of LANGUAGE, LC_ALL, LC_MESSAGES
    and LANG that the app has, else English."""
    environment = os.environ if environment is None else environment
    forced = environment.get(OVERRIDE_VARIABLE, "")
    if forced and (language := language_of(forced)):
        return language
    candidates = [part for part in environment.get("LANGUAGE", "").split(":") if part]
    candidates += [environment[name] for name in LOCALE_VARIABLES if environment.get(name)]
    for candidate in candidates:
        if language := language_of(candidate):
            return language
    return "en"


class EnvironmentLocalizer:
    """The language of the desktop session."""

    def __init__(self, environment: Mapping[str, str] | None = None) -> None:
        self.language = detect_language(environment)


class FixedLocalizer:
    """A language chosen by the caller, for tests and tools."""

    def __init__(self, language: str = "en") -> None:
        if language not in LANGUAGES:
            raise ValueError(f"no strings in {language}")
        self.language = language
