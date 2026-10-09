"""A scenario for test_application.py, run in a process of its own: GApplication runs once per
process. It drives the real application with timers and prints OK, or what went wrong.

  python -m tests.application_scenario close-hides | close-quits
"""

import os
import sys

from gi.repository import GLib

from plaitway.app.application import PlaitwayApplication


def scenario_close(expect_hidden: bool):
    class Scenario(PlaitwayApplication):
        def do_activate(self) -> None:
            self.show_window()
            GLib.timeout_add(1500, self.verify)

        def verify(self) -> bool:
            window = self._window
            if expect_hidden != self._tray_available:
                self.fail(f"the tray is {'there' if self._tray_available else 'not there'}")
                return False
            window.close()
            GLib.timeout_add(600, self.after_close)
            return False

        def after_close(self) -> bool:
            if expect_hidden:
                if self._window.get_visible():
                    self.fail("the window stayed open although a tray shows the app")
                else:
                    print("OK", flush=True)
                self.hold_released = True
                self._quit()
            else:
                self.fail("the app is running although its window was closed and there is no tray")
                self._quit()
            return False

        def fail(self, message: str) -> None:
            print("FAILED:", message, flush=True)

        def do_shutdown(self) -> None:
            if not expect_hidden:
                print("OK", flush=True)
            PlaitwayApplication.do_shutdown(self)

    return Scenario()


def main() -> int:
    name = sys.argv[1]
    application = {"close-hides": lambda: scenario_close(True), "close-quits": lambda: scenario_close(False)}[name]()
    return application.run([sys.argv[0]])


if __name__ == "__main__":
    sys.exit(main())
