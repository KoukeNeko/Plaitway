"""The window: a sidebar of profiles and a page at its side, or the helper's setup
while it is not ready. It collapses to one pane at narrow widths."""

from __future__ import annotations

import gi

gi.require_version("Gtk", "4.0")
gi.require_version("Adw", "1")
gi.require_version("Gdk", "4.0")
from gi.repository import Adw, Gdk, Gio, GLib, Gtk  # noqa: E402

from ..client.types import ProfileState  # noqa: E402
from ..core.app_model import (  # noqa: E402
    DIAGNOSTICS,
    SETTINGS,
    AppModel,
    DiagnosticsPage,
    ProfileSection,
    SelectionKind,
)
from ..core.app_state import WindowGeometry  # noqa: E402
from ..core.setup import SetupState  # noqa: E402
from .paths import APP_NAME  # noqa: E402
from .pages.app_settings import AppSettingsPage  # noqa: E402
from .pages.diagnostics import build_diagnostics_pages  # noqa: E402
from .pages.profile_page import build_profile_pages  # noqa: E402
from .pages.setup import SetupPage, empty_profiles_page  # noqa: E402
from .sidebar import Sidebar  # noqa: E402
from .widgets import PageSwitcher, follow, icon_button, name_widget  # noqa: E402

NARROW = "max-width: 640sp"
MIN_WIDTH = 360
MIN_HEIGHT = 400
SAVE_DELAY_MS = 500


class MainWindow(Adw.ApplicationWindow):
    def __init__(self, application: Adw.Application, model: AppModel, geometry: WindowGeometry) -> None:
        super().__init__(
            application=application, title=APP_NAME, default_width=geometry.width, default_height=geometry.height
        )
        self._model = model
        self._strings = model.strings
        self._narrow = False
        self._syncing = False
        self._profile_pages: dict[str, Adw.ViewStack] = {}
        self._geometry_save = 0
        self._current_stack: Adw.ViewStack | None = None
        self._stack_signal: tuple | None = None
        if geometry.maximized:
            self.maximize()
        self.set_size_request(MIN_WIDTH, MIN_HEIGHT)

        self._build_content()
        # A list takes the focus by selecting its first row, so the window starts with the focus
        # on a button: what is selected is the person's choice, or the first profile.
        self.set_focus(self._import_button)
        self._build_actions()
        self._build_drop_target()
        self._build_breakpoint()
        for name in ("default-width", "default-height", "maximized"):
            self.connect(f"notify::{name}", self._geometry_changed)

        follow(self, [model.changed], self.update)

    # MARK: Building

    def _build_content(self) -> None:
        self._root = Gtk.Stack(transition_type=Gtk.StackTransitionType.CROSSFADE)
        self.set_content(self._root)

        self._setup = SetupPage(model=self._model)
        setup_view = Adw.ToolbarView()
        setup_header = Adw.HeaderBar()
        setup_header.set_show_title(False)
        setup_view.add_top_bar(setup_header)
        setup_view.set_content(self._setup)
        self._root.add_named(setup_view, "setup")

        self._split = Adw.NavigationSplitView(min_sidebar_width=250, max_sidebar_width=320, sidebar_width_fraction=0.28)
        self._split.set_sidebar(self._build_sidebar())
        self._split.set_content(self._build_page())
        self._root.add_named(self._split, "main")

    def _build_sidebar(self) -> Adw.NavigationPage:
        s = self._strings
        self._sidebar = Sidebar(self._model, lambda: self._split.set_show_content(True))
        header = Adw.HeaderBar()
        self._import_button = icon_button("list-add-symbolic", s.import_profile, self._model.choose_import)
        header.pack_start(self._import_button)
        header.set_title_widget(Adw.WindowTitle(title=APP_NAME))
        header.pack_end(self._build_menu_button())
        view = Adw.ToolbarView()
        view.add_top_bar(header)
        view.set_content(self._sidebar)
        return Adw.NavigationPage.new(view, s.profiles)

    def _build_menu_button(self) -> Gtk.MenuButton:
        s = self._strings
        menu = Gio.Menu()
        actions = Gio.Menu()
        actions.append(s.import_profile, "app.import")
        actions.append(s.disconnect_all, "app.disconnect-all")
        menu.append_section(None, actions)
        pages = Gio.Menu()
        pages.append(s.diagnostics, "win.diagnostics")
        pages.append(s.open_settings, "app.settings")
        menu.append_section(None, pages)
        app = Gio.Menu()
        app.append(s.about_plaitway, "app.about")
        app.append(s.quit_plaitway, "app.quit")
        menu.append_section(None, app)
        button = Gtk.MenuButton(icon_name="open-menu-symbolic", menu_model=menu, primary=True, tooltip_text=s.main_menu)
        name_widget(button, s.main_menu)
        return button

    def _build_page(self) -> Adw.NavigationPage:
        s = self._strings
        self._title = Adw.WindowTitle(title=APP_NAME, subtitle="")
        self._switcher = PageSwitcher(self._page_chosen)
        # Only the child that is shown counts for the size: the toggles are wider than a narrow window.
        self._header_title = Gtk.Stack(transition_type=Gtk.StackTransitionType.NONE, hhomogeneous=False)
        self._header_title.add_named(self._title, "title")
        self._header_title.add_named(self._switcher, "switcher")
        header = Adw.HeaderBar(title_widget=self._header_title)

        self._banner = Adw.Banner(revealed=False)
        self._banner.connect("button-clicked", lambda *_: self._banner_clicked())
        self._banner_action = None

        # Only the page that is shown counts for the width: another page may need more than a narrow window has.
        self._content = Gtk.Stack(vexpand=True, transition_type=Gtk.StackTransitionType.CROSSFADE, hhomogeneous=False)
        self._content.add_named(empty_profiles_page(self._model), "empty")
        self._diagnostics = build_diagnostics_pages(self._model)
        self._content.add_named(self._diagnostics, "diagnostics")
        self._content.add_named(AppSettingsPage(self._model), "settings")

        self._switcher_bar = Adw.ViewSwitcherBar()
        self._actions = _ProfileActionBar(self._model)

        view = Adw.ToolbarView()
        view.add_top_bar(header)
        view.add_top_bar(self._banner)
        view.set_content(self._content)
        view.add_bottom_bar(self._switcher_bar)
        view.add_bottom_bar(self._actions)
        self._page = Adw.NavigationPage.new(view, APP_NAME)
        return self._page

    def _build_breakpoint(self) -> None:
        breakpoint_ = Adw.Breakpoint.new(Adw.BreakpointCondition.parse(NARROW))
        # One pane at a time: the sidebar, or the page of what is selected.
        breakpoint_.add_setter(self._split, "collapsed", True)
        breakpoint_.connect("apply", lambda *_: self._set_narrow(True))
        breakpoint_.connect("unapply", lambda *_: self._set_narrow(False))
        self.add_breakpoint(breakpoint_)

    def _set_narrow(self, narrow: bool) -> None:
        self._narrow = narrow
        self._model.set_compact(narrow)
        self.update()

    def _build_actions(self) -> None:
        model = self._model

        def add(name: str, callback, parameter: GLib.VariantType | None = None) -> Gio.SimpleAction:
            action = Gio.SimpleAction.new(name, parameter)
            action.connect("activate", lambda _, value: callback(value))
            self.add_action(action)
            return action

        self._toggle = add("toggle-connection", lambda _: self._toggle_selected())
        add("profile-toggle", lambda id_: self._with_profile(id_.get_string(), model.toggle), GLib.VariantType("s"))
        add("profile-move-up", lambda id_: model.move_by(id_.get_string(), -1), GLib.VariantType("s"))
        add("profile-move-down", lambda id_: model.move_by(id_.get_string(), 1), GLib.VariantType("s"))
        add("profile-delete", lambda id_: model.request_deletion(id_.get_string()), GLib.VariantType("s"))
        add("show-page", lambda index: self.show_page(index.get_int32()), GLib.VariantType("i"))
        self._find = add("find", lambda _: model.request_search())
        add("diagnostics", lambda _: self.show_diagnostics())
        self._resync = add("resync", lambda _: model.resync())
        self._move_up = add("move-up", lambda _: self._move_selected(-1))
        self._move_down = add("move-down", lambda _: self._move_selected(1))
        self._delete = add("delete-profile", lambda _: self._delete_selected())

    def _build_drop_target(self) -> None:
        target = Gtk.DropTarget.new(Gdk.FileList, Gdk.DragAction.COPY)
        target.connect("enter", lambda *_: self._drop_over(True) or Gdk.DragAction.COPY)
        target.connect("leave", lambda *_: self._drop_over(False))
        target.connect("drop", self._dropped)
        self._root.add_controller(target)

    # MARK: Actions

    def _with_profile(self, profile_id: str, command) -> None:
        profile = self._model.store.profile(profile_id)
        if profile is not None:
            command(profile)

    def _toggle_selected(self) -> None:
        profile = self._model.selected_profile
        if profile is not None and profile.state != ProfileState.DISCONNECTING:
            self._model.toggle(profile)

    def _move_selected(self, delta: int) -> None:
        profile = self._model.selected_profile
        if profile is not None:
            self._model.move_by(profile.id, delta)

    def _delete_selected(self) -> None:
        profile = self._model.selected_profile
        if profile is not None:
            self._model.request_deletion(profile.id)

    def show_page(self, index: int) -> None:
        """Ctrl+1 to Ctrl+5: a page of the profile that is open, or of Diagnostics."""
        selection = self._model.selection
        if selection is not None and selection.kind is SelectionKind.DIAGNOSTICS:
            pages = list(DiagnosticsPage)
            if 0 <= index < len(pages):
                self._model.show_diagnostics_page(pages[index])
        elif self._model.store.profiles:
            sections = list(ProfileSection)
            if 0 <= index < len(sections):
                self._model.show_section(sections[index])
                self._split.set_show_content(True)

    def show_diagnostics(self) -> None:
        self._model.select(DIAGNOSTICS)
        self._split.set_show_content(True)

    def show_settings(self) -> None:
        self._model.select(SETTINGS)
        self._split.set_show_content(True)

    def _dropped(self, target: Gtk.DropTarget, files, x: float, y: float) -> bool:
        self._drop_over(False)
        paths = [file.get_path() for file in files.get_files() if file.get_path()]
        if not paths:
            return False
        self._model.import_files(paths)
        return True

    def _drop_over(self, over: bool) -> None:
        if over:
            self._root.add_css_class("drop-over")
        else:
            self._root.remove_css_class("drop-over")

    # MARK: Model to view

    def update(self) -> None:
        model = self._model
        usable = model.setup.is_usable
        showing_setup = model.setup_content is not None
        self._root.set_visible_child_name("setup" if showing_setup else "main")
        if showing_setup:
            return

        self._prune_pages()
        selection = model.selection
        profile = model.selected_profile
        if selection is None or (selection.kind is SelectionKind.PROFILE and profile is None):
            self._content.set_visible_child_name("empty")
            self._use_stack(None)
            title, subtitle = APP_NAME, ""
        elif selection.kind is SelectionKind.DIAGNOSTICS:
            self._content.set_visible_child_name("diagnostics")
            self._use_stack(self._diagnostics)
            title, subtitle = self._strings.diagnostics, ""
        elif selection.kind is SelectionKind.SETTINGS:
            self._content.set_visible_child_name("settings")
            self._use_stack(None)
            title, subtitle = self._strings.settings, ""
        else:
            page = self._profile_pages.get(profile.id)
            if page is None:
                page = build_profile_pages(model, profile.id)
                self._profile_pages[profile.id] = page
                self._content.add_named(page, f"profile:{profile.id}")
            self._content.set_visible_child_name(f"profile:{profile.id}")
            self._use_stack(page)
            title, subtitle = profile.name, model.presenter.profile_subtitle(profile)

        self._title.set_title(title)
        self._title.set_subtitle(subtitle)
        self._page.set_title(title)
        self.set_title(title)

        has_pages = model.has_pages
        self._header_title.set_visible_child_name("switcher" if has_pages and not self._narrow else "title")
        self._switcher_bar.set_reveal(has_pages and self._narrow)
        self._actions.set_visible(profile is not None and selection is not None and selection.kind is SelectionKind.PROFILE)
        self._sync_stack_page()
        self._update_banner()
        self._update_actions(usable, profile)

    def _prune_pages(self) -> None:
        existing = set(self._model.store.profiles.ids)
        for profile_id in [i for i in self._profile_pages if i not in existing]:
            page = self._profile_pages.pop(profile_id)
            if page is self._current_stack:
                self._use_stack(None)
            self._content.remove(page)

    def _use_stack(self, stack: Adw.ViewStack | None) -> None:
        """The page switcher follows the pages of what is selected."""
        if stack is self._current_stack:
            return
        if self._stack_signal is not None:
            old, handler = self._stack_signal
            old.disconnect(handler)
            self._stack_signal = None
        self._current_stack = stack
        self._switcher_bar.set_stack(stack)
        if stack is not None:
            handler = stack.connect("notify::visible-child-name", self._stack_page_changed)
            self._stack_signal = (stack, handler)

    def _sync_stack_page(self) -> None:
        """The pages of a profile and of Diagnostics are the model's."""
        stack = self._current_stack
        if stack is None:
            return
        strings = self._strings
        if stack is self._diagnostics:
            name = self._model.diagnostics_page.name.lower()
            pages = [(page.name.lower(), page.label(strings)) for page in DiagnosticsPage]
        else:
            name = self._model.profile_section.name.lower()
            pages = [(section.name.lower(), section.label(strings)) for section in ProfileSection]
        self._switcher.show_pages(pages, name)
        if stack.get_visible_child_name() != name:
            self._syncing = True
            try:
                stack.set_visible_child_name(name)
            finally:
                self._syncing = False

    def _stack_page_changed(self, stack: Adw.ViewStack, _) -> None:
        """The bar at the bottom of a narrow window chose a page."""
        if not self._syncing and stack.get_visible_child_name():
            self._page_chosen(stack.get_visible_child_name())

    def _page_chosen(self, name: str) -> None:
        if self._current_stack is self._diagnostics:
            self._model.show_diagnostics_page(DiagnosticsPage[name.upper()])
        else:
            self._model.show_section(ProfileSection[name.upper()])

    def _update_actions(self, usable: bool, profile) -> None:
        selection = self._model.selection
        on_profile = profile is not None and usable
        self._toggle.set_enabled(on_profile and profile.state != ProfileState.DISCONNECTING)
        self._find.set_enabled(
            usable and selection is not None and (
                selection.kind is SelectionKind.DIAGNOSTICS
                or (profile is not None and self._model.profile_section in (ProfileSection.LOGS,))
            )
        )
        self._resync.set_enabled(usable)
        ids = self._model.store.profiles.ids
        self._move_up.set_enabled(on_profile and ids[:1] != [profile.id])
        self._move_down.set_enabled(on_profile and ids[-1:] != [profile.id])
        self._delete.set_enabled(on_profile)

    # MARK: Notices about the helper

    def _update_banner(self) -> None:
        s = self._strings
        setup = self._model.setup
        if setup.state is SetupState.VERSION_MISMATCH:
            self._banner.set_title(f"{s.helper_out_of_date}  {setup.daemon_version} → {setup.app_version}")
            self._banner.set_button_label(s.restart_helper if self._model.can_manage_helper else None)
            self._banner_action = "restart"
            self._banner.set_revealed(True)
        elif setup.state is SetupState.NOT_RESPONDING:
            self._banner.set_title(s.helper_unavailable)
            self._banner.set_button_label(s.retry)
            self._banner_action = "retry"
            self._banner.set_revealed(True)
        else:
            self._banner.set_revealed(False)

    def _banner_clicked(self) -> None:
        if self._banner_action == "restart":
            self._model.request_restart_helper()
        elif self._banner_action == "retry":
            self._model.retry()

    # MARK: Remembering the size

    def _geometry_changed(self, *_) -> None:
        if self._geometry_save:
            GLib.source_remove(self._geometry_save)
        self._geometry_save = GLib.timeout_add(SAVE_DELAY_MS, self.remember_geometry)

    def remember_geometry(self) -> bool:
        self._geometry_save = 0
        maximized = self.is_maximized()
        previous = self._model.app_state.window
        width, height = (previous.width, previous.height) if maximized else self.get_default_size()
        self._model.app_state.set_window(WindowGeometry(width, height, maximized))
        return GLib.SOURCE_REMOVE


class _ProfileActionBar(Gtk.ActionBar):
    """What can be done with the profile as a whole, at the bottom of each of its
    pages: connect or disconnect, one action at a time, named for what pressing it
    does; and Retry for a profile that failed."""

    def __init__(self, model: AppModel) -> None:
        super().__init__()
        self._model = model
        s = model.strings
        self._retry = Gtk.Button(label=s.retry)
        self._retry.connect("clicked", lambda _: self._with_profile(lambda p: model.set_enabled(True, p.id)))
        self._toggle = Gtk.Button(label=s.connect)
        self._toggle.connect("clicked", lambda _: self._with_profile(model.toggle))
        self.pack_end(self._toggle)
        self.pack_end(self._retry)
        follow(self, [model.changed], self.update)

    def _with_profile(self, command) -> None:
        profile = self._model.selected_profile
        if profile is not None:
            command(profile)

    def update(self) -> None:
        profile = self._model.selected_profile
        if profile is None:
            return
        s = self._model.strings
        self._retry.set_visible(profile.state == ProfileState.FAILED)
        self._toggle.set_label(s.disconnect if profile.desired_enabled else s.connect)
        self._toggle.set_sensitive(profile.state != ProfileState.DISCONNECTING)
        if profile.desired_enabled:
            self._toggle.remove_css_class("suggested-action")
        else:
            self._toggle.add_css_class("suggested-action")
