"""The application: one instance per user (the application id), a window that is
hidden rather than closed while a tray shows the app, and the actions and
accelerators of the app."""

from __future__ import annotations

import logging
import signal

import gi

gi.require_version("Gtk", "4.0")
gi.require_version("Adw", "1")
gi.require_version("Gdk", "4.0")
from gi.repository import Adw, Gdk, Gio, GLib, Gtk  # noqa: E402

from ..core.app_model import AppModel  # noqa: E402
from ..version import __version__  # noqa: E402
from . import bootstrap, paths, style  # noqa: E402
from .tray.controller import TrayCommands, TrayController  # noqa: E402
from .window import MainWindow  # noqa: E402

log = logging.getLogger(__name__)

# How long a background start waits to hear that there is a tray before it shows its window instead.
TRAY_GRACE_MS = 1500

ACCELERATORS = {
    "app.quit": ["<Control>q"],
    "app.import": ["<Control>i"],
    "app.settings": ["<Control>comma"],
    "app.disconnect-all": ["<Control><Shift>k"],
    "win.toggle-connection": ["<Control>k"],
    "win.find": ["<Control>f"],
    "win.diagnostics": ["<Control><Shift>d"],
    "win.resync": ["<Control>r"],
    "win.move-up": ["<Control><Alt>Up"],
    "win.move-down": ["<Control><Alt>Down"],
    "win.delete-profile": ["<Control>BackSpace"],
    **{f"win.show-page(int32 {index})": [f"<Control>{index + 1}"] for index in range(5)},
}


class PlaitwayApplication(Adw.Application):
    def __init__(self) -> None:
        super().__init__(application_id=paths.APP_ID, flags=Gio.ApplicationFlags.HANDLES_OPEN)
        self.add_main_option("version", ord("v"), GLib.OptionFlags.NONE, GLib.OptionArg.NONE, "Print the version", None)
        self.add_main_option(
            "background", 0, GLib.OptionFlags.NONE, GLib.OptionArg.NONE,
            "Start in the tray without opening the window", None,
        )
        self._services: bootstrap.Services | None = None
        self._window: MainWindow | None = None
        self._tray: TrayController | None = None
        self._tray_available = False
        self._held = False
        self._background = False
        self._quitting = False

    @property
    def model(self) -> AppModel:
        return self._services.model

    # MARK: Lifecycle

    def do_handle_local_options(self, options: GLib.VariantDict) -> int:
        if options.contains("version"):
            print(f"plaitway-app {__version__}")
            return 0
        self._background = options.contains("background")
        if self._background:
            self.register(None)
            if self.get_is_remote():
                # Already running: a start at login has nothing to raise.
                return 0
        return -1

    def do_startup(self) -> None:
        Adw.Application.do_startup(self)
        style.install()
        if paths.from_checkout():
            Gtk.IconTheme.get_for_display(Gdk.Display.get_default()).add_search_path(str(paths.data_dir() / "icons"))
        Gtk.Window.set_default_icon_name(paths.APP_ID)
        self._services = bootstrap.build(self._dialog_parent)
        self._add_actions()
        for action, accelerators in ACCELERATORS.items():
            self.set_accels_for_action(action, accelerators)
        for number in (signal.SIGINT, signal.SIGTERM):
            GLib.unix_signal_add(GLib.PRIORITY_DEFAULT, number, self._terminated)

        self._tray = TrayController(
            self.model,
            TrayCommands(
                open_window=self.show_window,
                import_profile=lambda: self.activate_action("import", None),
                open_settings=lambda: self.activate_action("settings", None),
                disconnect_all=lambda: self.activate_action("disconnect-all", None),
                quit=lambda: self.activate_action("quit", None),
            ),
            # GNOME's tray extension searches only this directory, for files named like the icon, and
            # does not read a theme tree below it.
            str(paths.data_dir() / "icons" / "hicolor" / "symbolic" / "apps") if paths.from_checkout() else "",
            self._tray_availability,
        )
        self.model.start()
        self.model.changed.connect(self._model_changed)
        self._tray.start()
        self._tray.set_wanted(self.model.show_tray_icon)
        self._update_actions()

    def do_activate(self) -> None:
        if self._background:
            self._background = False
            # The tray may be a moment in coming; until it is known the app is held, and the
            # window opens instead if there is none.
            self.hold()
            GLib.timeout_add(TRAY_GRACE_MS, self._background_resolved)
            return
        self.show_window()

    def do_open(self, files, n_files: int, hint: str) -> None:
        self.show_window()
        self.model.import_files([f.get_path() for f in files if f.get_path()])

    def do_shutdown(self) -> None:
        if self._tray is not None:
            self._tray.stop()
        if self._services is not None:
            self._services.shutdown()
        Adw.Application.do_shutdown(self)

    def _terminated(self) -> bool:
        """SIGINT and SIGTERM end the app at once: tunnels stay up, the helper keeps them."""
        self._quit()
        return GLib.SOURCE_REMOVE

    def _background_resolved(self) -> bool:
        if not self._tray_available:
            self.show_window()
        self.release()
        return GLib.SOURCE_REMOVE

    # MARK: The window

    def _dialog_parent(self) -> Gtk.Window:
        """Dialogs belong to the window, which they bring back if it is hidden."""
        return self.show_window()

    def show_window(self) -> MainWindow:
        if self._window is None:
            self._window = MainWindow(self, self.model, self.model.app_state.window)
            self._window.connect("close-request", self._close_requested)
        self._window.present()
        return self._window

    def _close_requested(self, window: Gtk.Window) -> bool:
        """With a tray the window is hidden and the app stays; without one, closing it quits."""
        if self._quitting:
            return False
        window.remember_geometry()
        if self._tray_available:
            window.set_visible(False)
        else:
            self._request_quit()
        return True

    # MARK: The tray

    def _tray_availability(self, available: bool) -> None:
        self._tray_available = available
        if available and not self._held:
            self.hold()
            self._held = True
        elif not available and self._held:
            self.release()
            self._held = False
        # An app with no window and no tray cannot be reached.
        if not available and self._services is not None and self._window is not None and not self._window.get_visible():
            self.show_window()

    def _model_changed(self) -> None:
        self._tray.set_wanted(self.model.show_tray_icon)
        self._update_actions()
        if self.model.needs_window and (self._window is None or not self._window.get_visible()):
            self.show_window()

    # MARK: Actions

    def _add_actions(self) -> None:
        def add(name: str, callback) -> Gio.SimpleAction:
            action = Gio.SimpleAction.new(name, None)
            action.connect("activate", lambda *_: callback())
            self.add_action(action)
            return action

        add("quit", self._request_quit)
        self._import = add("import", self._import_profile)
        add("settings", self._open_settings)
        self._disconnect_all = add("disconnect-all", lambda: self.model.disconnect_all())
        add("about", self._show_about)

    def _update_actions(self) -> None:
        self._import.set_enabled(self.model.setup.is_usable)
        self._disconnect_all.set_enabled(bool(self.model.switched_on_profiles))

    def _import_profile(self) -> None:
        self.show_window()
        self.model.choose_import()

    def _open_settings(self) -> None:
        self.show_window().show_settings()

    def _show_about(self) -> None:
        dialog = Adw.AboutDialog(
            application_name=paths.APP_NAME,
            application_icon=paths.APP_ID,
            developer_name="KoukeNeko",
            version=__version__,
            website="https://github.com/KoukeNeko/Plaitway",
            license_type=Gtk.License.MIT_X11,
            copyright="© KoukeNeko",
        )
        dialog.present(self.show_window())

    # MARK: Quitting

    def _request_quit(self) -> None:
        """Quit asks what it needs to ask; the helper, not the app, keeps profiles connected."""
        self.model.request_quit(self._quit, blocked=self.show_window)

    def _quit(self) -> None:
        if self._quitting:
            return
        self._quitting = True
        if self._window is not None:
            self._window.remember_geometry()
        self.quit()
