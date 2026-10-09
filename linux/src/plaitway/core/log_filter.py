from __future__ import annotations

import enum

from ..client.types import LogLevel
from ..l10n.strings import Strings


class LogFilter(enum.Enum):
    """How much of a log to show: debug lines are most of what openvpn writes and hide the rest."""

    ALL = 0
    INFO = 1
    WARNINGS = 2
    ERRORS = 3

    def includes(self, level: int) -> bool:
        match self:
            case LogFilter.ALL:
                return True
            case LogFilter.INFO:
                return level != LogLevel.DEBUG
            case LogFilter.WARNINGS:
                return level in (LogLevel.WARN, LogLevel.ERROR)
            case LogFilter.ERRORS:
                return level == LogLevel.ERROR
        return True

    def label(self, strings: Strings) -> str:
        match self:
            case LogFilter.ALL:
                return strings.all_levels
            case LogFilter.INFO:
                return strings.info_and_above
            case LogFilter.WARNINGS:
                return strings.warnings_and_errors
            case _:
                return strings.errors_only


# Debug lines are hidden until the person asks for them.
DEFAULT_FILTER = LogFilter.INFO
