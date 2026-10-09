"""Keeps the secrets of a profile off the screen while it is edited.

The mask finds them in the profile text and shows a numbered placeholder such
as `‹secret 1›` in their place; `restore` puts each secret back where its
placeholder still stands after the user's edits.

What is a secret: a WireGuard `PrivateKey` or `PresharedKey` value (also in a
line that is commented out), the body of an OpenVPN `<key>`, `<tls-auth>`,
`<tls-crypt>`, `<tls-crypt-v2>`, `<pkcs12>`, `<secret>`, `<auth-user-pass>` or
`<http-proxy-user-pass>` block, and a PEM private key or OpenVPN static key
anywhere else in the text.

What an edit does to a placeholder: left alone, its secret comes back; moved
with cut and paste, the secret moves with it; deleted or typed over, the secret
is gone; copied, `restore` fails, because it cannot know where the secret
belongs; changed to a number the mask does not know, or damaged, it is ordinary
text.
"""

from __future__ import annotations

import re
from dataclasses import dataclass

from .config_text import BLANKS, OpenVPNLine, Range, closes_block, line_ranges, trimmed
from .types import ProfileKind

_INT64_MAX = 2**63 - 1

# Blocks whose body is a secret.
_SECRET_BLOCKS = frozenset({
    "key", "tls-auth", "tls-crypt", "tls-crypt-v2", "pkcs12", "secret", "auth-user-pass", "http-proxy-user-pass",
})

# Unicode White_Space. The daemon trims every part of a WireGuard line with
# Go's strings.TrimSpace, which takes in the no-break space, the ideographic
# space and the other Unicode white space, so a key behind a no-break space is
# a key the daemon accepts.
_WHITE_SPACE = frozenset(
    "\t\n\v\f\r \u0085             "
    "    　"
)

_PLACEHOLDER = re.compile(r"‹secret ([1-9][0-9]*)›")


class SecretMaskError(Exception):
    pass


class DuplicatedPlaceholder(SecretMaskError):
    """The text holds the placeholder of secret `number` more than once; `line`
    is where the second one stands."""

    def __init__(self, number: int, line: int) -> None:
        super().__init__(f"secret {number} appears more than once, at line {line}")
        self.number = number
        self.line = line


def _placeholder(number: int) -> str:
    return f"‹secret {number}›"


def _placeholders(text: str) -> list[tuple[Range, int]]:
    found = []
    for match in _PLACEHOLDER.finditer(text):
        number = int(match.group(1))
        if number <= _INT64_MAX:
            found.append((match.span(), number))
    return found


def placeholder_ranges(text: str) -> list[Range]:
    """Where `text` has something that looks like a placeholder, for the view to
    style. Whether the mask knows its number is another matter."""
    return [span for span, _ in _placeholders(text)]


@dataclass(frozen=True)
class _Expansion:
    line: int  # line of the displayed text that holds the placeholder
    extra_lines: int  # lines the secret adds when it replaces the placeholder


class Restoration:
    """The text with its secrets put back."""

    def __init__(self, text: str, expansions: list[_Expansion]) -> None:
        self.text = text
        self._expansions = expansions

    def display_line(self, restored_line: int) -> int:
        """The line of the displayed text that shows what stands on `restored_line`
        (1-based) of the restored text. The daemon names lines of the text it was
        sent, and a secret of several lines shows as one."""
        added = 0
        for expansion in self._expansions:
            first = expansion.line + added
            if restored_line < first:
                break
            if restored_line <= first + expansion.extra_lines:
                return expansion.line
            added += expansion.extra_lines
        return restored_line - added


class SecretMask:
    def __init__(self, text: str, kind: int) -> None:
        # A placeholder that is in the text already is not ours: skip its number.
        taken = {number for _, number in _placeholders(text)}
        display: list[str] = []
        secrets: dict[int, str] = {}
        cursor = 0
        number = 0
        for start, end in _secret_ranges(text, kind):
            number += 1
            while number in taken:
                number += 1
            display.append(text[cursor:start])
            display.append(_placeholder(number))
            secrets[number] = text[start:end]
            cursor = end
        display.append(text[cursor:])
        #: The text to show and edit.
        self.display_text = "".join(display)
        self._secrets = secrets

    def restore(self, edited: str) -> Restoration:
        """Replaces each placeholder of `edited` by its secret. Raises
        DuplicatedPlaceholder when a secret would have to be put in two places."""
        parts: list[str] = []
        expansions: list[_Expansion] = []
        restored: set[int] = set()
        cursor = 0
        line = 1
        scanned = 0
        for (start, end), number in _placeholders(edited):
            secret = self._secrets.get(number)
            if secret is None:
                continue
            line += edited.count("\n", scanned, start)
            scanned = start
            if number in restored:
                raise DuplicatedPlaceholder(number, line)
            restored.add(number)
            parts.append(edited[cursor:start])
            parts.append(secret)
            cursor = end
            expansions.append(_Expansion(line, secret.count("\n")))
        parts.append(edited[cursor:])
        return Restoration("".join(parts), expansions)


# MARK: Finding secrets


def _secret_ranges(text: str, kind: int) -> list[Range]:
    """The text to hide, in order of appearance and without overlap. A block
    that is never closed runs to the end of the text, as it does for OpenVPN."""
    lines = line_ranges(text)
    reads_wireguard = kind != ProfileKind.OPENVPN
    reads_openvpn = kind != ProfileKind.WIREGUARD
    found: list[Range] = []
    # The block whose lines are verbatim text and not directives.
    verbatim_block: str | None = None
    # Labels of PEM blocks that have no END line after the last place that was
    # looked: a text of many BEGIN lines and no END would otherwise be searched
    # to its end for each.
    without_footer: set[str] = set()
    index = 0
    while index < len(lines):
        line = lines[index]
        following = index + 1
        opened = OpenVPNLine.parse(text, line).opened_block(text) if verbatim_block is None and reads_openvpn else None
        if verbatim_block is not None and closes_block(text, verbatim_block, line):
            verbatim_block = None
        elif opened is not None:
            if opened in _SECRET_BLOCKS:
                closing = next(
                    (i for i in range(following, len(lines)) if closes_block(text, opened, lines[i])), len(lines)
                )
                if closing > following:
                    body = (lines[following][0], lines[closing - 1][1])
                    if text[body[0] : body[1]].strip(BLANKS + "\n"):
                        found.append(body)
                following = closing + 1
            elif opened != "connection":
                verbatim_block = opened
        elif verbatim_block is None and reads_wireguard and (value := _wireguard_secret(text, line)):
            found.append(value)
        elif pem := _private_key_pem(text, index, lines, without_footer):
            found.append(pem[0])
            following = pem[1] + 1
        index = following
    return found


def _wireguard_secret(text: str, line: Range) -> Range | None:
    """The value of a `PrivateKey` or `PresharedKey` line. A commented-out key
    is as secret as a live one."""
    cursor, end = line
    while cursor < end and (text[cursor] in _WHITE_SPACE or text[cursor] == "#"):
        cursor += 1
    name_end = cursor
    while name_end < end and text[name_end] != "=" and text[name_end] not in _WHITE_SPACE:
        name_end += 1
    if text[cursor:name_end].lower() not in ("privatekey", "presharedkey"):
        return None
    equals = name_end
    while equals < end and text[equals] in _WHITE_SPACE:
        equals += 1
    if equals >= end or text[equals] != "=":
        return None
    value_start = equals + 1
    hash_at = text.find("#", value_start, end)
    value_end = hash_at if hash_at >= 0 else end
    first, last = value_start, value_end
    while first < last and text[first] in _WHITE_SPACE:
        first += 1
    while first < last and text[last - 1] in _WHITE_SPACE:
        last -= 1
    return (first, last) if first < last else None


def _private_key_pem(
    text: str, first: int, lines: list[Range], without_footer: set[str]
) -> tuple[Range, int] | None:
    """A PEM private key or an OpenVPN static key that starts on line `first`:
    from its BEGIN line to its END line. Without an END line it is not one."""
    begin = trimmed(text, lines[first])
    header = text[begin[0] : begin[1]]
    if not header.startswith("-----BEGIN ") or not header.endswith("-----"):
        return None
    if len(header) <= len("-----BEGIN -----"):
        return None
    label = header[len("-----BEGIN ") : -len("-----")]
    upper_label = label.upper()
    if "PRIVATE KEY" not in upper_label and not upper_label.startswith("OPENVPN"):
        return None
    # No footer was found after an earlier BEGIN of this label, so there is none after this one.
    if label in without_footer:
        return None
    footer = f"-----END {label}-----"
    for last in range(first + 1, len(lines)):
        start, end = trimmed(text, lines[last])
        if text[start:end] == footer:
            return (begin[0], end), last
    without_footer.add(label)
    return None
