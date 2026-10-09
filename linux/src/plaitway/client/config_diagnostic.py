from __future__ import annotations

import re
from dataclasses import dataclass

from .errors import DaemonFailure, FailureKind

_REJECTION = re.compile(r"line ([0-9]+): (.*)", re.DOTALL)
_INT64_MAX = 2**63 - 1


@dataclass(frozen=True)
class ConfigDiagnostic:
    """A problem the daemon found in profile text, for the editor to mark. The
    daemon rejects a text with one reason, `line 12: route: "x" is not a
    netmask`, or without a line when the problem is the text as a whole
    (`profile has no remote server`) or its kind (`this profile is OpenVPN, but
    the text is WireGuard`).

    line: 1-based, counted in the text that was sent (with SecretMask, the
        restored text: see Restoration.display_line). None when the problem is
        not tied to a line.
    message: the daemon's reason, English and without its `line N: ` prefix.
    """

    line: int | None
    message: str

    @classmethod
    def from_rejection(cls, rejection: str) -> ConfigDiagnostic:
        match = _REJECTION.fullmatch(rejection)
        if match and 0 < int(match.group(1)) <= _INT64_MAX:
            return cls(int(match.group(1)), match.group(2))
        return cls(None, rejection)

    @classmethod
    def from_error(cls, error: BaseException) -> ConfigDiagnostic | None:
        """The diagnostic of an error from update_profile_content or
        import_profile. None when the daemon did not refuse the text: it is
        down, the caller is not an administrator, the profile is gone."""
        failure = DaemonFailure.from_error(error)
        if failure.kind is not FailureKind.REJECTED:
            return None
        return cls.from_rejection(failure.message)
