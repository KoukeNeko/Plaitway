"""The GNOME Shell extension that ships with the app (data/share/gnome-shell/extensions): where it stands on
this machine, and what the Tools page offers for it."""

from __future__ import annotations

import enum
from dataclasses import dataclass

UUID = "plaitway@koukeneko.github.io"


class ExtensionState(enum.Enum):
    #: There is no GNOME Shell to load it.
    UNAVAILABLE = "unavailable"
    NOT_INSTALLED = "not installed"
    DISABLED = "disabled"
    ENABLED = "enabled"


@dataclass(frozen=True)
class ExtensionStatus:
    state: ExtensionState
    #: A copy in the user's own folder, which the app made and can remove.
    user_installed: bool = False
    #: Installed or enabled, but the running Shell has not loaded it: it reads its extensions when it starts.
    needs_login: bool = False
    #: The running Shell has loaded it: its top bar item is on screen.
    active: bool = False

    @staticmethod
    def of(
        *, shell: bool, installed: bool, user_installed: bool, enabled: bool, loaded: bool | None
    ) -> ExtensionStatus:
        """`loaded` is None where the Shell cannot be asked."""
        if not shell:
            return ExtensionStatus(ExtensionState.UNAVAILABLE)
        if not installed:
            return ExtensionStatus(ExtensionState.NOT_INSTALLED)
        state = ExtensionState.ENABLED if enabled else ExtensionState.DISABLED
        return ExtensionStatus(state, user_installed, needs_login=enabled and loaded is False, active=enabled and loaded is True)
