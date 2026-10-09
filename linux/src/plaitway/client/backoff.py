"""How long a dropped watch waits before it asks again."""

from __future__ import annotations

import random
from dataclasses import dataclass


@dataclass(frozen=True)
class BackoffPolicy:
    """200 ms growing by 1.6 to 5 s with 20 % jitter, as the macOS app's transport."""

    initial: float = 0.2
    maximum: float = 5.0
    multiplier: float = 1.6
    jitter: float = 0.2

    def delay(self, attempt: int, rng: random.Random | None = None) -> float:
        """Seconds to wait before retry number `attempt`, counted from 0."""
        base = min(self.initial * self.multiplier**attempt, self.maximum)
        spread = (rng or random).uniform(-self.jitter, self.jitter)
        return base * (1 + spread)
