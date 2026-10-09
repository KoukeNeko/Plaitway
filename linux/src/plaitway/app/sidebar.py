"""The sidebar: the profiles on top, in priority order (drag to change it), and
Diagnostics and Settings below them whatever their number."""

from __future__ import annotations

from collections.abc import Callable

import gi

gi.require_version("Gtk", "4.0")
gi.require_version("Adw", "1")
gi.require_version("Gdk", "4.0")
from gi.repository import Gdk, Gio, GLib, GObject, Gtk, Pango  # noqa: E402

from ..client.types import Profile, ProfileState  # noqa: E402
from ..core.app_model import DIAGNOSTICS, SETTINGS, AppModel, Selection, SelectionKind  # noqa: E402
from .widgets import StateGlyph, follow, label, name_widget  # noqa: E402


class ProfileRow(Gtk.ListBoxRow):
    """A profile in the sidebar: its shield in the colour of its state, its name, and
    under it what it is and how it stands."""

    def __init__(self, profile_id: str) -> None:
        super().__init__()
        self.profile_id = profile_id
        box = Gtk.Box(spacing=10, margin_top=6, margin_bottom=6, margin_start=4, margin_end=4)
        self.glyph = StateGlyph(22)
        self.glyph.set_size_request(30, -1)
        box.append(self.glyph)
        words = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=1, valign=Gtk.Align.CENTER, hexpand=True)
        self.name = label(ellipsize=Pango.EllipsizeMode.MIDDLE)
        self.caption = label(css=["caption", "dimmed"], ellipsize=Pango.EllipsizeMode.END)
        words.append(self.name)
        words.append(self.caption)
        box.append(words)
        self.set_child(box)

    def show(self, profile: Profile, subtitle: str) -> None:
        self.glyph.show_profile_state(profile.state)
        self.name.set_text(profile.name)
        self.caption.set_text(subtitle)
        name_widget(self, f"{profile.name}, {subtitle}")


class FooterRow(Gtk.ListBoxRow):
    def __init__(self, selection: Selection, icon: str, title: str) -> None:
        super().__init__()
        self.selection = selection
        box = Gtk.Box(spacing=10, margin_top=6, margin_bottom=6, margin_start=4, margin_end=4)
        image = Gtk.Image(icon_name=icon, pixel_size=18, accessible_role=Gtk.AccessibleRole.PRESENTATION)
        image.set_size_request(30, -1)
        image.add_css_class("dimmed")
        box.append(image)
        box.append(label(title))
        self.set_child(box)
        name_widget(self, title)


class Sidebar(Gtk.Box):
    def __init__(self, model: AppModel, on_activated: Callable[[], None]) -> None:
        """on_activated: the person chose an entry; a window that shows one pane at a time moves on to it."""
        super().__init__(orientation=Gtk.Orientation.VERTICAL)
        self._model = model
        self._strings = model.strings
        self._on_activated = on_activated
        self._rows: dict[str, ProfileRow] = {}
        self._syncing = False
        self._drop_row: ProfileRow | None = None

        self._profiles = Gtk.ListBox(selection_mode=Gtk.SelectionMode.SINGLE)
        self._profiles.add_css_class("navigation-sidebar")
        name_widget(self._profiles, self._strings.profiles)
        self._profiles.connect("row-selected", self._profile_selected)
        self._profiles.connect("row-activated", lambda *_: self._on_activated())
        scrolled = Gtk.ScrolledWindow(child=self._profiles, vexpand=True, hscrollbar_policy=Gtk.PolicyType.NEVER)
        self.append(scrolled)

        delete = Gtk.ShortcutController()
        delete.set_scope(Gtk.ShortcutScope.LOCAL)
        delete.add_shortcut(Gtk.Shortcut.new(
            Gtk.ShortcutTrigger.parse_string("Delete"), Gtk.CallbackAction.new(lambda *_: self._delete_selected())
        ))
        self._profiles.add_controller(delete)

        self.append(Gtk.Separator())
        self._footer = Gtk.ListBox(selection_mode=Gtk.SelectionMode.SINGLE)
        self._footer.add_css_class("navigation-sidebar")
        self._diagnostics = FooterRow(DIAGNOSTICS, "preferences-system-symbolic", self._strings.diagnostics)
        self._settings = FooterRow(SETTINGS, "emblem-system-symbolic", self._strings.settings)
        self._footer.append(self._diagnostics)
        self._footer.append(self._settings)
        self._footer.connect("row-selected", self._footer_selected)
        self._footer.connect("row-activated", lambda *_: self._on_activated())
        self.append(self._footer)

        follow(self, [model.changed], self.update)

    # MARK: Model to view

    def update(self) -> None:
        profiles = list(self._model.store.profiles)
        ids = [profile.id for profile in profiles]
        if ids != [row.profile_id for row in self._current_rows()]:
            self._rebuild(ids)
        presenter = self._model.presenter
        for profile in profiles:
            self._rows[profile.id].show(profile, presenter.profile_subtitle(profile))
        self._select(self._model.selection)

    def _current_rows(self) -> list[ProfileRow]:
        rows = []
        row = self._profiles.get_first_child()
        while row is not None:
            rows.append(row)
            row = row.get_next_sibling()
        return rows

    def _rebuild(self, ids: list[str]) -> None:
        self._syncing = True
        try:
            for row in self._current_rows():
                self._profiles.remove(row)
            self._rows = {}
            for profile_id in ids:
                row = ProfileRow(profile_id)
                self._attach_gestures(row)
                self._rows[profile_id] = row
                self._profiles.append(row)
        finally:
            self._syncing = False

    def _select(self, selection: Selection | None) -> None:
        self._syncing = True
        try:
            if selection is not None and selection.kind is SelectionKind.PROFILE and selection.profile_id in self._rows:
                self._profiles.select_row(self._rows[selection.profile_id])
                self._footer.unselect_all()
            elif selection is not None and selection.kind is SelectionKind.DIAGNOSTICS:
                self._profiles.unselect_all()
                self._footer.select_row(self._diagnostics)
            elif selection is not None and selection.kind is SelectionKind.SETTINGS:
                self._profiles.unselect_all()
                self._footer.select_row(self._settings)
            else:
                self._profiles.unselect_all()
                self._footer.unselect_all()
        finally:
            self._syncing = False

    # MARK: View to model

    def _profile_selected(self, _, row: ProfileRow | None) -> None:
        if not self._syncing and row is not None:
            self._model.select(Selection.profile(row.profile_id))

    def _footer_selected(self, _, row: FooterRow | None) -> None:
        if not self._syncing and row is not None:
            self._model.select(row.selection)

    def _delete_selected(self) -> bool:
        profile = self._model.selected_profile
        if profile is not None:
            self._model.request_deletion(profile.id)
        return True

    # MARK: Gestures: the context menu, and dragging to reorder

    def _attach_gestures(self, row: ProfileRow) -> None:
        click = Gtk.GestureClick(button=Gdk.BUTTON_SECONDARY)
        click.connect("pressed", lambda gesture, _, x, y: self._popup_menu(row, x, y))
        row.add_controller(click)
        press = Gtk.GestureLongPress()
        press.connect("pressed", lambda gesture, x, y: self._popup_menu(row, x, y))
        row.add_controller(press)

        source = Gtk.DragSource(actions=Gdk.DragAction.MOVE)
        source.connect("prepare", lambda *_: Gdk.ContentProvider.new_for_value(row.profile_id))
        source.connect("drag-begin", lambda src, drag: self._drag_began(row, src, drag))
        source.connect("drag-end", lambda *_: row.remove_css_class("dragged"))
        row.add_controller(source)

        target = Gtk.DropTarget.new(GObject.TYPE_STRING, Gdk.DragAction.MOVE)
        target.connect("motion", lambda tgt, x, y: self._drag_over(row, y))
        target.connect("leave", lambda *_: self._clear_drop())
        target.connect("drop", lambda tgt, value, x, y: self._dropped(row, value, y))
        row.add_controller(target)

    def _drag_began(self, row: ProfileRow, source: Gtk.DragSource, drag) -> None:
        row.add_css_class("dragged")
        source.set_icon(Gtk.WidgetPaintable.new(row), 12, 12)

    def _drag_over(self, row: ProfileRow, y: float) -> Gdk.DragAction:
        self._clear_drop()
        below = y > row.get_height() / 2
        row.add_css_class("drop-below" if below else "drop-above")
        self._drop_row = row
        return Gdk.DragAction.MOVE

    def _clear_drop(self) -> None:
        if self._drop_row is not None:
            self._drop_row.remove_css_class("drop-above")
            self._drop_row.remove_css_class("drop-below")
            self._drop_row = None

    def _dropped(self, row: ProfileRow, profile_id: str, y: float) -> bool:
        below = y > row.get_height() / 2
        self._clear_drop()
        ids = self._model.store.profiles.ids
        if profile_id not in ids or row.profile_id not in ids:
            return False
        destination = ids.index(row.profile_id) + (1 if below else 0)
        self._model.move([ids.index(profile_id)], destination)
        return True

    def _popup_menu(self, row: ProfileRow, x: float, y: float) -> None:
        profile = self._model.store.profile(row.profile_id)
        if profile is None:
            return
        s = self._strings
        menu = Gio.Menu()
        if profile.state != ProfileState.DISCONNECTING:
            toggle = Gio.MenuItem.new(s.disconnect if profile.desired_enabled else s.connect, None)
            toggle.set_action_and_target_value("win.profile-toggle", GLib.Variant.new_string(profile.id))
            menu.append_item(toggle)
        order = Gio.Menu()
        for title, action in ((s.move_up, "win.profile-move-up"), (s.move_down, "win.profile-move-down")):
            item = Gio.MenuItem.new(title, None)
            item.set_action_and_target_value(action, GLib.Variant.new_string(profile.id))
            order.append_item(item)
        menu.append_section(None, order)
        delete = Gio.Menu()
        item = Gio.MenuItem.new(s.delete_profile, None)
        item.set_action_and_target_value("win.profile-delete", GLib.Variant.new_string(profile.id))
        delete.append_item(item)
        menu.append_section(None, delete)

        popover = Gtk.PopoverMenu.new_from_model(menu)
        popover.set_parent(row)
        popover.set_has_arrow(False)
        popover.set_pointing_to(Gdk.Rectangle(x=int(x), y=int(y), width=1, height=1))
        popover.connect("closed", lambda p: p.unparent())
        popover.popup()

