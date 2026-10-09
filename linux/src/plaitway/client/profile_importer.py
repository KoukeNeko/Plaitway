"""Reads a profile file for import.

The daemon accepts content only, never a path, so an OpenVPN profile that
refers to its certificates and keys by file name gets those files read here,
with the user's own permissions, and put inline as `<ca>`, `<cert>`, `<key>`
and the like. A profile is untrusted, so it may only name files inside its own
directory; `auth-user-pass <file>` becomes a bare `auth-user-pass` and the
file's user name and password go to the credential store.

cmd/plaitway does the same for the command line (importfile.go). This follows
the macOS app, which also takes the credentials of an auth-user-pass file.
"""

from __future__ import annotations

import os
import re
import stat
from dataclasses import dataclass

from .types import Credentials

# The daemon refuses larger profiles (profile.MaxContentSize); a hopeless
# upload need not go over the socket.
MAX_PROFILE_SIZE = 1 << 20
# Generous for a certificate chain or a key, small enough to rule out reading
# something that is not one.
MAX_REFERENCED_FILE_SIZE = 256 << 10

# Directives that name a file and have an inline <tag> form.
_INLINE_DIRECTIVES = frozenset({
    "ca", "cert", "key", "dh", "crl-verify", "extra-certs", "tls-auth", "tls-crypt", "tls-crypt-v2",
})

# What separates the lines of a file: CR LF counts once.
_LINE_BREAK = re.compile(r"\r\n|[\n\r\x0b\x0c\x85  ]")
# Unicode Zs and the tab.
_BLANKS = " \t               　"


class ProfileImportError(Exception):
    """A profile, or a file it refers to, cannot be imported."""


class Unreadable(ProfileImportError):
    """The profile or a file it refers to cannot be read."""

    def __init__(self, path: str, reason: str) -> None:
        super().__init__(f"cannot read {path}: {reason}")
        self.path = path
        self.reason = reason


class NotText(ProfileImportError):
    """A file is not text; profiles and the files they refer to are."""

    def __init__(self, path: str) -> None:
        super().__init__(f"not a text file: {path}")
        self.path = path


class TooLarge(ProfileImportError):
    def __init__(self, path: str) -> None:
        super().__init__(f"file too large: {path}")
        self.path = path


class _DirectiveFailure(ProfileImportError):
    def __init__(self, directive: str, path: str, text: str) -> None:
        super().__init__(f"{text}: {path}")
        self.directive = directive
        self.path = path


class MissingFile(_DirectiveFailure):
    """A directive names a file that does not exist."""

    def __init__(self, directive: str, path: str) -> None:
        super().__init__(directive, path, f"file for {directive} not found")


class NotKeyMaterial(_DirectiveFailure):
    """A directive names a file that holds no PEM data."""

    def __init__(self, directive: str, path: str) -> None:
        super().__init__(directive, path, f"file for {directive} holds no certificate or key")


class OutsideProfileDirectory(_DirectiveFailure):
    """A directive names a file that is not inside the profile's directory."""

    def __init__(self, directive: str, path: str) -> None:
        super().__init__(directive, path, f"file for {directive} is outside the profile's folder")


class NotCredentials(ProfileImportError):
    """An `auth-user-pass` file without a user name and a password on its first two lines."""

    def __init__(self, path: str) -> None:
        super().__init__(f"file for auth-user-pass holds no user name and password: {path}")
        self.path = path


@dataclass(frozen=True)
class Loaded:
    """A profile ready for DaemonClient.import_profile."""

    content: bytes  # the profile as the daemon takes it
    credentials: Credentials | None  # what the profile's auth-user-pass file held, if it named one


def load(path: str) -> Loaded:
    """The profile as it is sent to the daemon, with the credentials that came with it."""
    data = _read_text(path, MAX_PROFILE_SIZE)
    text = data.decode("utf-8")
    # A wg-quick file has no file references; its keys are inline already.
    if not _is_openvpn(text, os.path.basename(path)):
        return Loaded(data, None)
    inlined, credentials = inline_referenced_files(text, os.path.dirname(os.path.abspath(path)))
    result = inlined.encode("utf-8")
    if len(result) > MAX_PROFILE_SIZE:
        raise TooLarge(path)
    return Loaded(result, credentials)


def inline_referenced_files(text: str, directory: str) -> tuple[str, Credentials | None]:
    """Replaces `ca file`, `tls-auth file 1` and the like by their inline
    blocks, and `auth-user-pass file` by `auth-user-pass`, returning the file's
    credentials. Paths are relative to the profile's directory, as for openvpn,
    and must stay inside it. Blocks that are already inline are left alone.
    The result uses LF only."""
    lines = _LINE_BREAK.split(text)
    has_key_direction = any((_tokens(line) or [""])[0].lower() == "key-direction" for line in lines)

    output: list[str] = []
    # The profile is untrusted and may name the same 256 KiB file on every line
    # of 1 MiB: the size of the result is counted as it grows, not when it is done.
    size = 0

    def emit(piece: str, path: str) -> None:
        nonlocal size
        size += len(piece.encode("utf-8")) + 1
        if size > MAX_PROFILE_SIZE:
            raise TooLarge(path)
        output.append(piece)

    credentials: Credentials | None = None
    open_block: str | None = None
    for line in lines:
        trimmed = line.strip(_BLANKS)
        if open_block is not None:
            if trimmed.lower() == f"</{open_block}>":
                open_block = None
            emit(line, directory)
            continue
        if (tag := _opening_tag(trimmed)) is not None:
            open_block = tag
            emit(line, directory)
            continue
        if (credentials_file := _credentials_file(trimmed)) is not None:
            credentials = _read_credentials(credentials_file, directory)
            emit("auth-user-pass", directory)
            continue
        reference = _file_reference(trimmed)
        if reference is None:
            emit(line, directory)
            continue

        directive, path, key_direction = reference
        content = _read_key_material(directive, path, directory)
        emit(f"<{directive}>", path)
        emit(content, path)
        emit(f"</{directive}>", path)
        if key_direction is not None and not has_key_direction:
            emit(f"key-direction {key_direction}", directory)
            has_key_direction = True
    return "\n".join(output), credentials


# MARK: Parsing


def _is_openvpn(text: str, filename: str) -> bool:
    if "[interface]" in text.lower():
        return False
    if filename.lower().endswith(".ovpn"):
        return True
    return any(
        words and words[0].lower() in ("remote", "client")
        for words in (_tokens(line) for line in _LINE_BREAK.split(text))
    )


def _file_reference(line: str) -> tuple[str, str, str | None] | None:
    """The directive, file and key direction of a line that names a file."""
    words = _tokens(line)
    if len(words) < 2 or words[0].lower() not in _INLINE_DIRECTIVES:
        return None
    directive = words[0].lower()
    # `dh none` switches Diffie-Hellman off; `[inline]` says the data follows.
    if (directive == "dh" and words[1] == "none") or words[1] == "[inline]":
        return None
    direction = words[2] if directive == "tls-auth" and len(words) >= 3 else None
    return directive, words[1], direction


def _credentials_file(line: str) -> str | None:
    """The file of an `auth-user-pass file` line."""
    words = _tokens(line)
    if len(words) >= 2 and words[0].lower() == "auth-user-pass":
        return words[1]
    return None


def _opening_tag(line: str) -> str | None:
    """`<ca>` opens a block; `</ca>` and `<connection>` style tags with
    arguments do not matter here, only that the lines in between are data."""
    if not (line.startswith("<") and line.endswith(">")) or line.startswith("</"):
        return None
    tag = line[1:-1].lower()
    return None if not tag or " " in tag else tag


def _tokens(line: str) -> list[str]:
    """Splits a configuration line into words like openvpn: double or single
    quotes group, a backslash escapes, and a line that starts with `#` or `;`
    is a comment."""
    stripped = line.strip(_BLANKS)
    if not stripped or stripped[0] in "#;":
        return []
    words: list[str] = []
    current: list[str] = []
    in_word = False
    quote: str | None = None
    escaped = False
    for char in stripped:
        if escaped:
            current.append(char)
            escaped = False
        elif char == "\\" and quote != "'":
            escaped = True
            in_word = True
        elif quote is not None:
            if char == quote:
                quote = None
            else:
                current.append(char)
        elif char in "\"'":
            quote = char
            in_word = True
        elif char.isspace():
            if in_word:
                words.append("".join(current))
            current = []
            in_word = False
        else:
            current.append(char)
            in_word = True
    if in_word:
        words.append("".join(current))
    return words


# MARK: Reading


def _read_key_material(directive: str, path: str, directory: str) -> str:
    resolved = _resolve_inside(path, directive, directory)
    text = _read_text(resolved, MAX_REFERENCED_FILE_SIZE).decode("utf-8")
    if "-----BEGIN" not in text:
        raise NotKeyMaterial(directive, resolved)
    return text.replace("\r\n", "\n").strip()


def _read_credentials(path: str, directory: str) -> Credentials:
    """Like openvpn, the user name is the first line and the password the second."""
    resolved = _resolve_inside(path, "auth-user-pass", directory)
    text = _read_text(resolved, MAX_REFERENCED_FILE_SIZE).decode("utf-8")
    lines = _LINE_BREAK.split(text)
    if len(lines) < 2 or not lines[0] or not lines[1]:
        raise NotCredentials(resolved)
    return Credentials(username=lines[0], password=lines[1])


def _resolve_inside(path: str, directive: str, directory: str) -> str:
    """The file `path` names, symlinks resolved, when it is inside the profile's
    directory. openvpn takes paths relative to the profile, and nothing else is
    read here: the content goes to a root daemon, so a profile must not be able
    to pick up any other file of the user's (`key ~/.ssh/id_ed25519`)."""
    outside = OutsideProfileDirectory(directive, path)
    if path.startswith(("/", "~")):
        raise outside

    # `..` is resolved by name first, so that a file that does not exist is not
    # reported as missing when the profile asked for it outside.
    components: list[str] = []
    for part in path.split("/"):
        if part in ("", "."):
            continue
        if part == "..":
            if not components:
                raise outside
            components.pop()
        else:
            components.append(part)

    try:
        root = os.path.realpath(directory, strict=True)
    except OSError as error:
        raise Unreadable(path, error.strerror or str(error)) from error
    root_prefix = root if root.endswith("/") else root + "/"
    named = "/".join([root, *components])
    try:
        resolved = os.path.realpath(named, strict=True)
    except FileNotFoundError as error:
        raise MissingFile(directive, named) from error
    except OSError as error:
        raise Unreadable(path, error.strerror or str(error)) from error
    if not resolved.startswith(root_prefix):
        raise outside
    return resolved


def _read_text(path: str, max_size: int) -> bytes:
    """The bytes of a regular UTF-8 text file of at most `max_size` bytes. A
    profile decides which files get read: a pipe or a device such as /dev/zero
    never ends, so the kind of file is decided on the opened file, which cannot
    be swapped for another between the check and the read."""
    try:
        # O_NONBLOCK: opening a pipe for reading must not wait for a writer.
        descriptor = os.open(path, os.O_RDONLY | os.O_NONBLOCK | os.O_CLOEXEC)
    except OSError as error:
        raise Unreadable(path, error.strerror or str(error)) from error
    try:
        info = os.fstat(descriptor)
        if not stat.S_ISREG(info.st_mode):
            raise NotText(path)
        if info.st_size > max_size:
            raise TooLarge(path)
        with os.fdopen(descriptor, "rb", closefd=False) as file:
            data = file.read(max_size + 1)
    except OSError as error:
        raise Unreadable(path, error.strerror or str(error)) from error
    finally:
        os.close(descriptor)
    if len(data) > max_size:
        raise TooLarge(path)
    if b"\0" in data:
        raise NotText(path)
    try:
        data.decode("utf-8")
    except UnicodeDecodeError:
        raise NotText(path) from None
    return data
