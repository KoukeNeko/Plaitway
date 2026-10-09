from __future__ import annotations

from dataclasses import dataclass

from ..client.types import ImportWarning


@dataclass(frozen=True)
class ImportOutcome:
    """What came of one file."""

    filename: str
    profile_name: str = ""  # the profile it became; empty when it failed
    warnings: tuple[ImportWarning, ...] = ()  # what the daemon removed or ignored
    failure: str = ""  # why it was refused; empty when it was imported

    @property
    def imported(self) -> bool:
        return not self.failure


@dataclass(frozen=True)
class ImportReport:
    """What came of importing the files the person picked or dropped."""

    outcomes: tuple[ImportOutcome, ...]

    @property
    def needs_attention(self) -> bool:
        """Worth a dialog: a file failed or the daemon changed something in it."""
        return any(not outcome.imported or outcome.warnings for outcome in self.outcomes)
