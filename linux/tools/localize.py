#!/usr/bin/env python3
"""Generates src/plaitway/l10n/strings.py: the typed accessors of every string of the UI.

Sources, in this order of authority:
  macos/Sources/PlaitwayMenuBar/Resources/Localizable.xcstrings   the macOS app's strings
  linux/Resources/Linux.xcstrings                                  strings only Linux has
  linux/Resources/strings-manifest.json                            what of the macOS catalog Linux does
                                                                   not use, and the accessor names of
                                                                   strings with placeholders

The same English text is the same string on both platforms, so a string that
has a role on Linux is not written twice. Run it after changing any of the
three files:  python tools/localize.py [--check]
"""

from __future__ import annotations

import argparse
import json
import keyword
import re
import sys
from dataclasses import dataclass
from pathlib import Path

LINUX_DIR = Path(__file__).resolve().parent.parent
MACOS_CATALOG = LINUX_DIR.parent / "macos/Sources/PlaitwayMenuBar/Resources/Localizable.xcstrings"
LINUX_CATALOG = LINUX_DIR / "Resources/Linux.xcstrings"
MANIFEST = LINUX_DIR / "Resources/strings-manifest.json"
OUTPUT = LINUX_DIR / "src/plaitway/l10n/strings.py"

LANGUAGES = ("en", "zh-Hant")
MAX_SLUG_LENGTH = 32

# printf-style specifiers of the catalogs, with the type their argument has.
_SPECIFIER = re.compile(r"%(?:(\d+)\$)?(@|lld|d)")
_SPECIFIER_TYPES = {"@": "str", "lld": "int", "d": "int"}


@dataclass(frozen=True)
class Entry:
    key: str
    name: str
    params: tuple[tuple[str, str], ...]  # (name, type)
    translations: dict[str, str]  # language -> template with {0}, {1}...
    source: str  # "macOS" or "Linux"


def load_catalog(path: Path) -> dict[str, dict[str, str]]:
    """key -> language -> value, with the source language's value the key itself."""
    data = json.loads(path.read_text(encoding="utf-8"))
    if data["sourceLanguage"] != "en":
        raise SystemExit(f"{path}: the source language is not en")
    strings: dict[str, dict[str, str]] = {}
    for key, entry in data["strings"].items():
        values = {"en": key}
        for language, localization in entry.get("localizations", {}).items():
            unit = localization.get("stringUnit")
            if unit is None:
                raise SystemExit(f"{path}: {key!r} has a localization without a stringUnit ({language})")
            if unit["state"] != "translated":
                raise SystemExit(f"{path}: {key!r} is not translated into {language}")
            values[language] = unit["value"]
        strings[key] = values
    return strings


def load_manifest() -> dict:
    return json.loads(MANIFEST.read_text(encoding="utf-8"))


def specifiers(text: str) -> list[str]:
    return [match.group(2) for match in _SPECIFIER.finditer(text)]


def to_template(text: str) -> str:
    """`Shadowed by %@` as `Shadowed by {0}`; `%2$@ of %1$@` keeps its order."""
    counter = iter(range(1000))

    def replace(match: re.Match) -> str:
        position = int(match.group(1)) - 1 if match.group(1) else next(counter)
        return "{%d}" % position

    escaped = text.replace("{", "{{").replace("}", "}}")
    return _SPECIFIER.sub(replace, escaped)


def slug(key: str) -> str:
    return re.sub(r"[^a-z0-9]+", "_", key.lower()).strip("_")


def build_entries() -> list[Entry]:
    macos = load_catalog(MACOS_CATALOG)
    linux = load_catalog(LINUX_CATALOG)
    manifest = load_manifest()
    excluded = set(manifest["macosOnly"])
    accessors = manifest["accessors"]

    stray = excluded - macos.keys()
    if stray:
        raise SystemExit(f"strings-manifest.json: macosOnly names strings the macOS catalog lacks: {sorted(stray)}")
    repeated = linux.keys() & macos.keys()
    if repeated:
        raise SystemExit(f"Linux.xcstrings repeats strings of the macOS catalog: {sorted(repeated)}")
    unknown = accessors.keys() - (macos.keys() | linux.keys())
    if unknown:
        raise SystemExit(f"strings-manifest.json: accessors for strings no catalog has: {sorted(unknown)}")
    overlap = excluded & accessors.keys()
    if overlap:
        raise SystemExit(f"strings-manifest.json: excluded strings with an accessor: {sorted(overlap)}")

    entries: list[Entry] = []
    sources = [(key, values, "macOS") for key, values in macos.items() if key not in excluded]
    sources += [(key, values, "Linux") for key, values in linux.items()]
    for key, values, source in sources:
        missing = [language for language in LANGUAGES if language not in values]
        if missing:
            raise SystemExit(f"{key!r} is not translated into {missing}")
        kinds = specifiers(key)
        for language in LANGUAGES:
            if sorted(specifiers(values[language])) != sorted(kinds):
                raise SystemExit(f"{key!r} -> {values[language]!r}: the placeholders differ")
        accessor = accessors.get(key, {})
        name = accessor.get("name") or slug(key)
        params = accessor.get("params", [])
        if len(params) != len(kinds):
            raise SystemExit(f"{key!r} has {len(kinds)} placeholders: name them in strings-manifest.json (params)")
        if not accessor and len(name) > MAX_SLUG_LENGTH:
            raise SystemExit(f"{key!r}: the accessor {name!r} is long, name it in strings-manifest.json")
        if not name.isidentifier() or keyword.iskeyword(name):
            raise SystemExit(f"{key!r}: {name!r} is no identifier, name it in strings-manifest.json")
        entries.append(Entry(
            key=key,
            name=name,
            params=tuple(zip(params, (_SPECIFIER_TYPES[kind] for kind in kinds), strict=True)),
            translations={language: to_template(values[language]) for language in LANGUAGES},
            source=source,
        ))

    names: dict[str, str] = {}
    for entry in entries:
        if entry.name in names:
            raise SystemExit(f"{entry.key!r} and {names[entry.name]!r} both become {entry.name!r}: name one in strings-manifest.json")
        names[entry.name] = entry.key
    return sorted(entries, key=lambda entry: entry.name)


def render(entries: list[Entry]) -> str:
    out = [
        '"""Every string of the UI, as typed accessors. Generated by tools/localize.py: do not edit.',
        "",
        "The strings are those of the macOS app (macos/Sources/PlaitwayMenuBar/Resources/Localizable.xcstrings)",
        "that have a role on Linux, and linux/Resources/Linux.xcstrings.",
        '"""',
        "",
        "from __future__ import annotations",
        "",
        "# language -> accessor -> template; {0}, {1} are the arguments of the accessor.",
        "_TABLES: dict[str, dict[str, str]] = {",
    ]
    for language in LANGUAGES:
        out.append(f"    {language!r}: {{")
        for entry in entries:
            out.append(f"        {entry.name!r}: {entry.translations[language]!r},")
        out.append("    },")
    out += [
        "}",
        "",
        f"LANGUAGES = {LANGUAGES!r}",
        "",
        "",
        "class Strings:",
        '    """The strings in one language. An unknown language reads as English."""',
        "",
        '    def __init__(self, language: str = "en") -> None:',
        "        self.language = language if language in _TABLES else \"en\"",
        "        self._table = _TABLES[self.language]",
    ]
    for entry in entries:
        out.append("")
        out.append(f"    # {entry.source}: {entry.key}".replace("\n", " "))
        if entry.params:
            signature = ", ".join(f"{name}: {kind}" for name, kind in entry.params)
            arguments = ", ".join(name for name, _ in entry.params)
            out.append(f"    def {entry.name}(self, {signature}) -> str:")
            out.append(f"        return self._table[{entry.name!r}].format({arguments})")
        else:
            out.append("    @property")
            out.append(f"    def {entry.name}(self) -> str:")
            out.append(f"        return self._table[{entry.name!r}]")
    return "\n".join(out) + "\n"


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--check", action="store_true", help="fail when the committed module is stale")
    args = parser.parse_args()

    text = render(build_entries())
    if args.check:
        if not OUTPUT.exists() or OUTPUT.read_text(encoding="utf-8") != text:
            sys.exit(f"{OUTPUT} is stale: run tools/localize.py")
        return
    OUTPUT.write_text(text, encoding="utf-8")
    print(f"wrote {OUTPUT.relative_to(LINUX_DIR.parent)}")


if __name__ == "__main__":
    main()
