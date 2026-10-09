from __future__ import annotations

import enum
import socket
from dataclasses import dataclass

from .config_text import OpenVPNLine, Range, closes_block, line_ranges, trimmed
from .types import ProfileKind


class Role(enum.Enum):
    """The part a stretch of text plays. The role says what the text is; the view decides how it looks."""

    SECTION = "section"  # WireGuard: [Interface], [Peer]
    DIRECTIVE = "directive"  # a WireGuard key such as Address, an OpenVPN directive such as remote
    ARGUMENT = "argument"  # a value or parameter that has no more specific role
    NUMBER = "number"
    IP_ADDRESS = "ip address"
    CIDR = "cidr"  # an address with its prefix length, 10.0.0.0/8
    PORT = "port"
    HOSTNAME = "hostname"
    BASE64_KEY = "base64 key"  # WireGuard PrivateKey, PublicKey and PresharedKey values
    COMMENT = "comment"  # from # (or ; in OpenVPN) to the end of the line
    BLOCK_TAG = "block tag"  # OpenVPN <ca> and </ca>
    BLOCK_BODY = "block body"  # the text between the tags of an OpenVPN block, PEM data and all


@dataclass(frozen=True)
class ConfigToken:
    """A stretch of profile text, for syntax highlighting. It never holds a
    line break, except for a block body."""

    start: int
    end: int
    role: Role

    @property
    def range(self) -> Range:
        return self.start, self.end


def tokens(text: str, kind: int) -> list[ConfigToken]:
    """Splits profile text into tokens the way the daemon reads it. The text may
    be wrong or half-written: whatever has no role is left out, and the tokens
    come in order of position and never overlap. None for a kind that is not
    OpenVPN or WireGuard."""
    lexer = _Lexer(text)
    if kind == ProfileKind.OPENVPN:
        lexer.scan_openvpn()
    elif kind == ProfileKind.WIREGUARD:
        lexer.scan_wireguard()
    return lexer.tokens


def _is_ip_address(text: str) -> bool:
    for family in (socket.AF_INET, socket.AF_INET6):
        try:
            socket.inet_pton(family, text)
            return True
        except (OSError, ValueError):
            pass
    return False


def _is_number(text: str) -> bool:
    return bool(text) and all("0" <= char <= "9" for char in text)


def _address_role(text: str) -> Role | None:
    """IP_ADDRESS for an address, CIDR for an address with a prefix length."""
    if _is_ip_address(text):
        return Role.IP_ADDRESS
    address, slash, prefix = text.rpartition("/")
    if slash and _is_ip_address(address) and _is_number(prefix):
        return Role.CIDR
    return None


class _Lexer:
    def __init__(self, text: str) -> None:
        self.text = text
        self.tokens: list[ConfigToken] = []

    def add(self, span: Range | None, role: Role) -> None:
        if span is not None and span[0] < span[1]:
            self.tokens.append(ConfigToken(span[0], span[1], role))

    def slice(self, span: Range) -> str:
        return self.text[span[0] : span[1]]

    # MARK: WireGuard

    def scan_wireguard(self) -> None:
        for line in line_ranges(self.text):
            # The daemon cuts a line at the first `#`, wherever it stands.
            hash_at = self.text.find("#", line[0], line[1])
            comment = (hash_at, line[1]) if hash_at >= 0 else None
            content = trimmed(self.text, (line[0], comment[0] if comment else line[1]))
            if self.slice(content)[:1] == "[":
                self.add(content, Role.SECTION)
            else:
                equals = self.text.find("=", content[0], content[1])
                if equals >= 0:
                    key = trimmed(self.text, (content[0], equals))
                    self.add(key, Role.DIRECTIVE)
                    self._wireguard_value(self.slice(key).lower(), trimmed(self.text, (equals + 1, content[1])))
            self.add(comment, Role.COMMENT)

    def _wireguard_value(self, key: str, value: Range) -> None:
        match key:
            case "privatekey" | "publickey" | "presharedkey":
                self.add(value, Role.BASE64_KEY)
            case "address" | "allowedips":
                for item in self._items(value):
                    self.add(item, _address_role(self.slice(item)) or Role.ARGUMENT)
            case "dns":
                for item in self._items(value):
                    self.add(item, _address_role(self.slice(item)) or Role.HOSTNAME)
            case "endpoint":
                self._endpoint(value)
            case "listenport":
                self.add(value, Role.PORT if _is_number(self.slice(value)) else Role.ARGUMENT)
            case "mtu" | "persistentkeepalive" | "fwmark":
                self.add(value, Role.NUMBER if _is_number(self.slice(value)) else Role.ARGUMENT)
            case _:
                self.add(value, Role.ARGUMENT)

    def _endpoint(self, value: Range) -> None:
        """`host:port` or `[IPv6]:port`."""
        colon = self.text.rfind(":", value[0], value[1])
        if colon < 0:
            self.add(value, Role.ARGUMENT)
            return
        host = (value[0], colon)
        if self.slice(host)[:1] == "[" and self.slice(host)[-1:] == "]":
            host = (host[0] + 1, host[1] - 1)
        port = (colon + 1, value[1])
        self.add(host, Role.IP_ADDRESS if _is_ip_address(self.slice(host)) else Role.HOSTNAME)
        self.add(port, Role.PORT if _is_number(self.slice(port)) else Role.ARGUMENT)

    def _items(self, value: Range) -> list[Range]:
        """The comma separated items of `value`, without their blanks."""
        items: list[Range] = []
        start = value[0]
        while True:
            comma = self.text.find(",", start, value[1])
            end = comma if comma >= 0 else value[1]
            item = trimmed(self.text, (start, end))
            if item[0] < item[1]:
                items.append(item)
            if comma < 0:
                return items
            start = comma + 1

    # MARK: OpenVPN

    def scan_openvpn(self) -> None:
        # The block whose lines are verbatim: its tag and the lines seen in it so far.
        block: tuple[str, Range | None] | None = None
        for line in line_ranges(self.text):
            if block is not None:
                tag, body = block
                if closes_block(self.text, tag, line):
                    self.add(body, Role.BLOCK_BODY)
                    self.add(trimmed(self.text, line), Role.BLOCK_TAG)
                    block = None
                else:
                    block = (tag, ((body[0] if body else line[0]), line[1]))
                continue
            parsed = OpenVPNLine.parse(self.text, line)
            tag = parsed.opened_block(self.text)
            if tag is not None:
                self.add(parsed.fields[0], Role.BLOCK_TAG)
                self.add(parsed.comment, Role.COMMENT)
                # The body of a <connection> block is directives.
                if tag != "connection":
                    block = (tag, None)
            else:
                self._directive(parsed)
        # A block that is never closed runs to the end, as it does for OpenVPN.
        if block is not None:
            self.add(block[1], Role.BLOCK_BODY)

    def _directive(self, line: OpenVPNLine) -> None:
        if line.fields:
            name = line.fields[0]
            word = self.slice(name)
            is_closing_tag = len(line.fields) == 1 and word.startswith("</") and word.endswith(">")
            self.add(name, Role.BLOCK_TAG if is_closing_tag else Role.DIRECTIVE)
            keyword = word.lstrip("-").lower()
            for index, field in enumerate(line.fields[1:]):
                self.add(field, self._argument_role(index, keyword, self.slice(field)))
        self.add(line.comment, Role.COMMENT)

    @staticmethod
    def _argument_role(index: int, directive: str, field: str) -> Role:
        word = field
        if len(word) > 1 and word[0] in "\"'" and word[-1] == word[0]:
            word = word[1:-1]
        if (directive, index) in {("remote", 0), ("http-proxy", 0), ("socks-proxy", 0)}:
            return Role.IP_ADDRESS if _is_ip_address(word) else Role.HOSTNAME
        if (directive, index) in {
            ("remote", 1), ("http-proxy", 1), ("socks-proxy", 1), ("port", 0), ("lport", 0), ("rport", 0),
        }:
            return Role.PORT if _is_number(word) else Role.ARGUMENT
        return _address_role(word) or (Role.NUMBER if _is_number(word) else Role.ARGUMENT)
