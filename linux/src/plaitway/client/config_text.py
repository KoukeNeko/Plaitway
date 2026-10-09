"""What SecretMask and ConfigTokenizer share: reading profile text as lines,
the way the daemon does.

A range is (start, end), offsets into the str in code points, which are what a
Gtk.TextBuffer counts. Everything that carries meaning in a profile is ASCII,
so text in any other script passes through untouched.
"""

from __future__ import annotations

import re
from dataclasses import dataclass

BOM = "﻿"

Range = tuple[int, int]

# The blanks of a profile line: space, tab and carriage return.
BLANKS = " \t\r"


def line_ranges(text: str) -> list[Range]:
    """The lines of the text, each without its line break (LF or CR LF). The
    text after the last line break is a line only when it is not empty. A byte
    order mark at the start belongs to no line, as for the daemon."""
    lines: list[Range] = []
    start = 1 if text.startswith(BOM) else 0
    length = len(text)
    while start < length:
        line_feed = text.find("\n", start)
        if line_feed < 0:
            lines.append((start, length))
            break
        end = line_feed
        if end > start and text[end - 1] == "\r":
            end -= 1
        lines.append((start, end))
        start = line_feed + 1
    return lines


def trimmed(text: str, line: Range) -> Range:
    """`line` without the blanks at both ends."""
    start, end = line
    while start < end and text[start] in BLANKS:
        start += 1
    while end > start and text[end - 1] in BLANKS:
        end -= 1
    return start, end


@dataclass(frozen=True)
class OpenVPNLine:
    """One OpenVPN line split into parameters with the rules of OpenVPN's own
    parser (and the daemon's): blanks separate them, a parameter that starts
    with a double or single quote runs to the closing quote, a backslash escapes
    the next character outside single quotes, and a `#` or `;` where a
    parameter would start begins a comment that runs to the end of the line."""

    fields: tuple[Range, ...]
    comment: Range | None

    _TAG = re.compile(r"[A-Za-z0-9_-]+")

    @classmethod
    def parse(cls, text: str, line: Range) -> OpenVPNLine:
        fields: list[Range] = []
        comment: Range | None = None
        cursor, end = line
        while cursor < end:
            char = text[cursor]
            if char in BLANKS:
                cursor += 1
            elif char in "#;":
                comment = (cursor, end)
                break
            else:
                start = cursor
                quote = char if char in "\"'" else None
                if quote:
                    cursor += 1
                while cursor < end:
                    current = text[cursor]
                    if quote:
                        if current == quote:
                            cursor += 1
                            break
                    elif current in BLANKS:
                        break
                    if current == "\\" and quote != "'":
                        if cursor + 1 < end:
                            cursor += 1
                    cursor += 1
                fields.append((start, cursor))
        return cls(tuple(fields), comment)

    def opened_block(self, text: str) -> str | None:
        """The name of the block that a line opens: a lone `<tag>` of letters,
        digits, `-` and `_`, optionally followed by a comment. Lower-case."""
        if len(self.fields) != 1:
            return None
        start, end = self.fields[0]
        word = text[start:end]
        if len(word) > 1 and word[0] in "\"'" and word[-1] == word[0]:
            word = word[1:-1]
        if len(word) < 3 or not word.startswith("<") or not word.endswith(">"):
            return None
        tag = word[1:-1]
        return tag.lower() if self._TAG.fullmatch(tag) else None


def closes_block(text: str, tag: str, line: Range) -> bool:
    """Whether the line closes the block `tag` (lower-case): `</tag>` and nothing else."""
    start, end = trimmed(text, line)
    return text[start:end].lower() == f"</{tag}>"
