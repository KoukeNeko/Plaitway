"""Numbers and times as the UI writes them."""

from __future__ import annotations

from datetime import datetime, tzinfo

from ..l10n.strings import Strings

_UNITS = ("B", "kB", "MB", "GB", "TB", "PB", "EB", "ZB")


def format_bytes(count: int) -> str:
    """Decimal units, as the file manager counts them; nothing is spelled out ("0 B")."""
    if count < 1000:
        return f"{count} B"
    value = float(count)
    unit = 0
    while value >= 1000 and unit < len(_UNITS) - 1:
        value /= 1000
        unit += 1
    return f"{value:.1f} {_UNITS[unit]}"


def format_rate(bytes_per_second: float, strings: Strings) -> str:
    return strings.rate_per_second(format_bytes(round(bytes_per_second)))


def format_uptime(seconds: float) -> str:
    """H:MM:SS, hours as many as there are."""
    total = max(0, int(seconds))
    hours, rest = divmod(total, 3600)
    minutes, secs = divmod(rest, 60)
    return f"{hours}:{minutes:02d}:{secs:02d}"


def format_endpoint(host: str, port: int, protocol: str) -> str:
    """`host:port (tcp)` for an endpoint of a profile."""
    text = host if port == 0 else f"{host}:{port}"
    return f"{text} ({protocol})" if protocol else text


def format_log_time(timestamp: float, tz: tzinfo | None = None) -> str:
    """A time of day that is the same width in every language, so that log lines line up."""
    return datetime.fromtimestamp(timestamp, tz).strftime("%H:%M:%S")


def format_date_time(timestamp: float, tz: tzinfo | None = None) -> str:
    return datetime.fromtimestamp(timestamp, tz).strftime("%Y-%m-%d %H:%M:%S")
