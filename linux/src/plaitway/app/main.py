"""plaitway-app: the entry point."""

from __future__ import annotations

import logging
import os
import sys


def main(argv: list[str] | None = None) -> int:
    logging.basicConfig(
        level=logging.DEBUG if os.environ.get("PLAITWAY_DEBUG") else logging.INFO,
        format="%(levelname)s %(name)s: %(message)s",
    )
    from .application import PlaitwayApplication

    return PlaitwayApplication().run(sys.argv if argv is None else argv)
