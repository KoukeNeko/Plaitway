#!/usr/bin/env python3
"""Renders the app's window to PNG files, in-process, for every page, in light and
dark, at a wide and a narrow width, in one language.

It runs the real application against a development daemon (`plaitwayd -fake`, started
here on a private socket) and draws the window's own widget tree with the GSK
renderer, so what is saved is what GTK drew. It opens real windows, so it needs a
display.

  python tools/capture_ui.py --language zh-Hant --out /tmp/shots [--page overview] [--scheme dark]

The PLAITWAY_DAEMON variable names a prebuilt plaitwayd, as for the tests.
"""

from __future__ import annotations

import argparse
import os
import shutil
import sys
import tempfile
from pathlib import Path

LINUX_DIR = Path(__file__).resolve().parent.parent
sys.path[:0] = [str(LINUX_DIR / "src"), str(LINUX_DIR)]

SIZES = {"wide": (1040, 660), "narrow": (400, 720)}
# A picture of the window that compresses to less than this is one flat colour.
BLANK_PICTURE_BYTES = 3000


def parse_arguments() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--language", default="en", choices=["en", "zh-Hant"])
    parser.add_argument("--out", default="/tmp/plaitway-shots")
    parser.add_argument("--scheme", action="append", choices=["light", "dark"], help="default: both")
    parser.add_argument("--size", action="append", choices=list(SIZES), help="default: both")
    parser.add_argument("--page", action="append", help="only the shots whose name contains this")
    return parser.parse_args()


def main() -> int:
    arguments = parse_arguments()
    from tests.support import DaemonProcess, openvpn_profile, wireguard_profile

    daemon = DaemonProcess(fake=True)
    state_home = tempfile.mkdtemp(prefix="pw-shots-")
    try:
        daemon.start()
        # The environment is read when the app starts: the language, the daemon, the state.
        os.environ.update({
            "PLAITWAY_DEV": "1",
            "PLAITWAY_SOCKET": daemon.socket_path,
            "PLAITWAY_LANGUAGE": arguments.language,
            "XDG_STATE_HOME": state_home,
            "XDG_CONFIG_HOME": state_home,
        })
        seed_profiles(daemon.socket_path, openvpn_profile, wireguard_profile)
        write_state(state_home)
        run(arguments, daemon)
    finally:
        daemon.cleanup()
        shutil.rmtree(state_home, ignore_errors=True)
    return 0


def write_state(state_home: str) -> None:
    """No tray item while the shots are made: it would show up on the desktop."""
    directory = Path(state_home) / "plaitway"
    directory.mkdir(parents=True, exist_ok=True)
    (directory / "state.json").write_text('{"show_tray_icon": false}\n')


def seed_profiles(socket_path: str, openvpn_profile, wireguard_profile) -> None:
    """Four profiles in the states a person sees: two connected, one failed, one idle."""
    from plaitway.client.daemon_client import DaemonClient
    from plaitway.client.types import ProfileSettings

    client = DaemonClient(socket_path)
    try:
        office = client.import_profile(
            openvpn_profile(host="vpn.office.example.net", extra=["dhcp-option DNS 10.20.0.1"]), "office.ovpn", name="Office"
        ).profile.id
        home = client.import_profile(wireguard_profile(), "home.conf", name="Home Lab").profile.id
        cafe = client.import_profile(openvpn_profile(markers=["# fake: fail"]), "cafe.ovpn", name="Café").profile.id
        client.import_profile(
            openvpn_profile(host="203.0.113.7", markers=["# fake: needs-credentials"]), "depot.ovpn", name="Depot",
            settings=ProfileSettings(auto_connect=False),
        )
        client.set_profile_enabled(office, True)
        client.set_profile_enabled(home, True)
        client.set_profile_enabled(cafe, True)
    finally:
        client.close()


# MARK: The shots


def run(arguments: argparse.Namespace, daemon) -> None:
    import dataclasses

    import gi

    gi.require_version("Gtk", "4.0")
    gi.require_version("Adw", "1")
    gi.require_version("Graphene", "1.0")
    from gi.repository import Adw, GLib, Graphene, Gtk

    from plaitway.app.application import PlaitwayApplication
    from plaitway.app.window import MainWindow
    from plaitway.client.errors import DaemonFailure, FailureKind
    from plaitway.core.app_model import DIAGNOSTICS, SETTINGS, DiagnosticsPage, ProfileSection, Selection
    from plaitway.core.app_state import WindowGeometry
    from plaitway.core.fakes import FakeHelperService
    from plaitway.core.ports import HelperRegistration

    out = Path(arguments.out) / arguments.language
    out.mkdir(parents=True, exist_ok=True)
    schemes = arguments.scheme or ["light", "dark"]
    sizes = arguments.size or list(SIZES)

    class CaptureApplication(PlaitwayApplication):
        def do_activate(self) -> None:
            # An application with no window ends: this one makes its windows as it goes.
            self.hold()
            GLib.idle_add(drive, self.shoot())

        def shoot(self):
            model = self.model
            for size in sizes:
                width, height = SIZES[size]
                window = MainWindow(self, model, WindowGeometry(width, height))
                self._window = window
                window.present()
                yield 1.0
                for scheme in schemes:
                    Adw.StyleManager.get_default().set_color_scheme(
                        Adw.ColorScheme.FORCE_DARK if scheme == "dark" else Adw.ColorScheme.FORCE_LIGHT
                    )
                    yield 0.5
                    yield from self.scenes(window, size, scheme)
                window.destroy()
                yield 0.3
            if "wide" in sizes:
                width, height = SIZES["wide"]
                window = MainWindow(self, model, WindowGeometry(width, height))
                self._window = window
                window.present()
                yield 1.0
                yield from self.final_scenes(window)
                window.destroy()
                yield 0.3
            self.quit()

        def profile(self, name: str) -> str:
            return next(p.id for p in self.model.store.profiles if p.name == name)

        def photograph(self, window, size: str, scheme: str, name: str):
            """Saves the window as <size>-<scheme>-<name>.png once it has settled."""
            if arguments.page and not any(part in name for part in arguments.page):
                return
            yield 0.7
            # The window's first child holds the content and the dialogs above it.
            content = window.get_first_child()
            # A machine under load can be slow to draw the first page: a picture that is
            # one flat colour compresses to a few hundred bytes, and is taken again.
            for attempt in range(6):
                width, height = content.get_width(), content.get_height()
                snapshot = Gtk.Snapshot()
                # The window paints its own background, which the content does not carry.
                found, background = window.get_style_context().lookup_color("window_bg_color")
                if found:
                    snapshot.append_color(background, Graphene.Rect().init(0, 0, width, height))
                Gtk.WidgetPaintable.new(content).snapshot(snapshot, width, height)
                node = snapshot.to_node()
                if node is None:
                    print(f"nothing was drawn for {name}")
                    return
                renderer = window.get_native().get_renderer()
                texture = renderer.render_texture(node, Graphene.Rect().init(0, 0, width, height))
                if texture.save_to_png_bytes().get_size() > BLANK_PICTURE_BYTES or attempt == 5:
                    break
                yield 0.7
            path = out / f"{size}-{scheme}-{name}.png"
            texture.save_to_png(str(path))
            print(path)

        def scenes(self, window, size: str, scheme: str):
            model = self.model

            def shot(name):
                return self.photograph(window, size, scheme, name)

            def close_dialog():
                dialog = window.get_visible_dialog()
                if dialog is not None:
                    dialog.force_close()

            # Both connected profiles have been up long enough to have a graph.
            for _ in range(40):
                office = self.profile("Office")
                if len(model.traffic.samples(office)) >= 4:
                    break
                yield 0.5

            if size == "narrow":
                model.select(Selection.profile(office))
                window._split.set_show_content(False)
                yield from shot("sidebar")
                window._split.set_show_content(True)
                yield from shot("overview")
                model.show_section(ProfileSection.SETTINGS)
                yield from shot("settings")
                model.show_section(ProfileSection.OVERVIEW)
                return

            model.select(Selection.profile(office))
            for section in ProfileSection:
                model.show_section(section)
                yield from shot(f"office-{section.name.lower()}")
            model.show_section(ProfileSection.OVERVIEW)
            model.select(Selection.profile(self.profile("Home Lab")))
            yield from shot("home-overview")
            model.select(Selection.profile(self.profile("Café")))
            yield from shot("cafe-overview")
            model.show_section(ProfileSection.ROUTES)
            yield from shot("cafe-routes")
            model.show_section(ProfileSection.OVERVIEW)

            model.select(Selection.profile(office))
            model.show_section(ProfileSection.CONFIGURATION)
            yield 0.5
            editor = model.editor(model.store.profile(office))
            editor.toggle_secrets()
            yield from shot("office-configuration-secrets")
            editor.toggle_secrets()
            editor.set_text(editor.text + "# fake: reject\n")
            editor.save(False, True)
            yield 1.0
            yield from shot("office-configuration-error")
            editor.revert()
            model.show_section(ProfileSection.OVERVIEW)

            model.select(DIAGNOSTICS)
            model.show_diagnostics_page(DiagnosticsPage.OVERVIEW)
            yield 1.5
            yield from shot("diagnostics")
            model.show_diagnostics_page(DiagnosticsPage.HELPER_LOG)
            yield from shot("diagnostics-helper-log")
            model.show_diagnostics_page(DiagnosticsPage.OVERVIEW)
            model.select(SETTINGS)
            yield from shot("app-settings")

            model.select(Selection.profile(office))
            model.request_deletion(office)
            yield from shot("delete-profile")
            close_dialog()
            yield 0.3
            model.request_remove_stale_route(model.store.client.get_diagnostics().stale_routes[0])
            yield from shot("remove-route")
            close_dialog()
            yield 0.3

            scratch = Path(tempfile.mkdtemp(prefix="pw-shots-files-"))
            (scratch / "scripted.ovpn").write_text("client\nremote vpn.example.net 1194\nup /bin/sh\n")
            (scratch / "bad.ovpn").write_text("client\n# fake: reject\n")
            (scratch / "notes.ovpn").write_text("client\nca missing.crt\n")
            model.import_files([str(scratch / name) for name in ("scripted.ovpn", "bad.ovpn", "notes.ovpn")])
            yield 1.5
            yield from shot("import-report")
            close_dialog()
            yield 0.3
            shutil.rmtree(scratch, ignore_errors=True)
            for profile in model.store.profiles:
                if profile.name in ("scripted",):
                    client = model.store.client
                    client.delete_profile(profile.id)
            yield 0.5

            depot = self.profile("Depot")
            model.select(Selection.profile(depot))
            model.set_enabled(True, depot)
            yield 1.5
            yield from shot("credentials-dialog")
            self.platform_cancel(depot)
            yield 1.0

        def final_scenes(self, window):
            """What the window shows with no profile, and with no helper to talk to. These end the
            run: the profiles are deleted and the daemon is stopped."""
            model = self.model
            client = model.store.client
            for profile in client.list_profiles():
                client.delete_profile(profile.id)
            model.select(None)
            yield 1.0
            for scheme in schemes:
                Adw.StyleManager.get_default().set_color_scheme(
                    Adw.ColorScheme.FORCE_DARK if scheme == "dark" else Adw.ColorScheme.FORCE_LIGHT
                )
                yield 0.5
                yield from self.photograph(window, "wide", scheme, "empty")

            daemon.kill()
            for _ in range(40):
                if model.store.connection.value == "unavailable":
                    break
                yield 0.25
            # The helper is a unit of this system: the fake stands for systemd, and the daemon is not the developer's.
            helper = FakeHelperService()
            model.platform = dataclasses.replace(model.platform, helper=helper)
            model.is_overridden = False
            cases = [
                ("not-installed", HelperRegistration.NOT_INSTALLED, None),
                ("not-running", HelperRegistration.STOPPED, None),
                ("not-answering", HelperRegistration.RUNNING, None),
                ("permission-denied", HelperRegistration.RUNNING, DaemonFailure(FailureKind.PERMISSION_DENIED, "")),
                ("not-trusted", HelperRegistration.RUNNING,
                 DaemonFailure(FailureKind.SERVER_REFUSED, "/run/plaitway is writable by others")),
            ]
            for scheme in schemes:
                Adw.StyleManager.get_default().set_color_scheme(
                    Adw.ColorScheme.FORCE_DARK if scheme == "dark" else Adw.ColorScheme.FORCE_LIGHT
                )
                for name, registration, failure in cases:
                    helper.current = registration
                    model.refresh_helper()
                    model._end_settling()
                    if failure is not None:
                        model.store._mark_unavailable(failure)
                    else:
                        model.store._mark_unavailable(DaemonFailure(FailureKind.UNAVAILABLE, ""))
                    yield 0.3
                    yield from self.photograph(window, "wide", scheme, f"setup-{name}")
                model.is_overridden = True
                yield 0.3
                yield from self.photograph(window, "wide", scheme, "setup-development")
                model.is_overridden = False

        def platform_cancel(self, profile_id: str) -> None:
            self.model.cancel_credentials(profile_id)

    def drive(generator) -> bool:
        """Runs a generator of delays on the main loop: each `yield n` waits n seconds."""

        def step() -> bool:
            try:
                delay = next(generator)
            except StopIteration:
                return GLib.SOURCE_REMOVE
            GLib.timeout_add(int(delay * 1000), step)
            return GLib.SOURCE_REMOVE

        step()
        return GLib.SOURCE_REMOVE

    app = CaptureApplication()
    app.run([sys.argv[0]])


if __name__ == "__main__":
    sys.exit(main())
