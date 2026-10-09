"""A live log: a profile's, or the helper's own for an empty id. It follows the
newest line while it is scrolled to the end and stays where it is once the person
scrolls up to read."""

from __future__ import annotations

import gi

gi.require_version("Gtk", "4.0")
gi.require_version("Adw", "1")
from gi.repository import GLib, Gtk  # noqa: E402

from ...core.app_model import AppModel  # noqa: E402
from ...core.log_filter import DEFAULT_FILTER, LogFilter  # noqa: E402
from ...core.log_tail import CAPACITY, TRIM_SLACK, LogEntry, LogTail  # noqa: E402
from ...core.presentation import Tone, log_tone  # noqa: E402
from ..widgets import follow, icon_button, name_widget, page_bar, status_page  # noqa: E402
from ..theme import TextPalette  # noqa: E402

SCROLL_SLACK = 8  # pixels from the end that still count as the end


class LogsPage(Gtk.Box):
    def __init__(self, model: AppModel, profile_id: str) -> None:
        super().__init__(orientation=Gtk.Orientation.VERTICAL)
        self._model = model
        self._profile_id = profile_id
        self._strings = model.strings
        self._tail: LogTail | None = None
        self._disconnect = None
        self._filter = DEFAULT_FILTER
        self._query = ""
        self._shown_id = -1
        self._shown_count = 0
        self._at_end = True
        self._follow_scheduled = False
        self._last_search_request = model.search_request

        self._build_bar()
        self._build_view()
        self._palette = TextPalette(self._view, self._tags)
        self.connect("map", self._start)
        self.connect("unmap", self._stop)
        follow(self, [model.changed], self._search_requested)

    # MARK: Building

    def _build_bar(self) -> None:
        s = self._strings
        bar = page_bar()
        self._search = Gtk.SearchEntry(placeholder_text=s.search_logs, hexpand=True)
        name_widget(self._search, s.search_logs)
        self._search.connect("search-changed", self._query_changed)
        bar.append(self._search)

        self._levels = Gtk.DropDown.new_from_strings([f.label(s) for f in LogFilter])
        self._levels.set_selected(list(LogFilter).index(self._filter))
        self._levels.set_tooltip_text(s.level)
        name_widget(self._levels, s.level)
        self._levels.connect("notify::selected", self._filter_changed)
        bar.append(self._levels)

        self._latest = icon_button("go-bottom-symbolic", s.latest, self._scroll_to_latest)
        bar.append(self._latest)
        self._copy = icon_button("edit-copy-symbolic", s.copy, self._copy_lines)
        bar.append(self._copy)
        self.append(bar)

    def _build_view(self) -> None:
        self._stack = Gtk.Stack(vexpand=True)
        self._empty = status_page("text-x-generic-symbolic", self._strings.no_log_lines)
        self._stack.add_named(self._empty, "empty")
        self._view = Gtk.TextView(editable=False, cursor_visible=False, wrap_mode=Gtk.WrapMode.WORD_CHAR, monospace=True)
        self._view.add_css_class("log-view")
        name_widget(self._view, self._strings.logs)
        self._buffer = self._view.get_buffer()
        self._tags = {
            Tone.WARNING: self._buffer.create_tag("warning"),
            Tone.ERROR: self._buffer.create_tag("error"),
            Tone.MUTED: self._buffer.create_tag("muted"),
            "time": self._buffer.create_tag("time"),
        }
        self._end_mark = self._buffer.create_mark("end", self._buffer.get_end_iter(), False)
        scrolled = Gtk.ScrolledWindow(child=self._view)
        self._adjustment = scrolled.get_vadjustment()
        self._adjustment.connect("value-changed", self._scrolled)
        self._adjustment.connect("changed", self._content_grew)
        self._stack.add_named(scrolled, "lines")
        self.append(self._stack)

    # MARK: Following the log

    def _start(self, *_) -> None:
        if self._tail is not None:
            return
        self._tail = self._model.log_tail(self._profile_id)
        self._disconnect = self._tail.changed.connect(self._refresh)
        self._tail.start()
        self._reset()
        self._refresh()

    def _stop(self, *_) -> None:
        if self._tail is None:
            return
        self._disconnect()
        self._tail.stop()
        self._tail = None

    def _reset(self) -> None:
        self._buffer.set_text("")
        self._shown_id = -1
        self._shown_count = 0
        self._at_end = True

    def _visible(self, entry: LogEntry) -> bool:
        return self._filter.includes(entry.level) and (not self._query or self._query.lower() in entry.text.lower())

    def _refresh(self) -> None:
        """Shows what came since the last time, or everything when the lines shown
        are not a prefix of what the tail holds any more."""
        if self._tail is None:
            return
        fresh, dropped = self._tail.entries_after(self._shown_id)
        if dropped or self._shown_count > CAPACITY + TRIM_SLACK:
            self._rebuild()
            return
        if fresh:
            self._shown_id = fresh[-1].id
            self._append([entry for entry in fresh if self._visible(entry)])
        self._update_state()

    def _rebuild(self) -> None:
        self._reset()
        entries = self._tail.entries if self._tail else []
        self._shown_id = entries[-1].id if entries else -1
        self._append([entry for entry in entries if self._visible(entry)])
        self._update_state()

    def _append(self, entries: list[LogEntry]) -> None:
        for entry in entries:
            end = self._buffer.get_end_iter()
            if self._shown_count:
                self._buffer.insert(end, "\n")
                end = self._buffer.get_end_iter()
            start_offset = end.get_offset()
            self._buffer.insert(end, entry.plain_text)
            self._style_line(entry, start_offset)
            self._shown_count += 1
        if entries:
            self._buffer.move_mark(self._end_mark, self._buffer.get_end_iter())
            if self._at_end:
                self._schedule_follow()

    def _style_line(self, entry: LogEntry, start_offset: int) -> None:
        """The time is dimmed, and the level and text take the colour of the level."""
        time_length = len(entry.time_text)
        if time_length:
            self._buffer.apply_tag(
                self._tags["time"],
                self._buffer.get_iter_at_offset(start_offset),
                self._buffer.get_iter_at_offset(start_offset + time_length),
            )
        tone = log_tone(entry.level)
        if tone in (Tone.WARNING, Tone.ERROR, Tone.MUTED):
            body = start_offset + (time_length + 1 if time_length else 0)
            self._buffer.apply_tag(
                self._tags[tone], self._buffer.get_iter_at_offset(body), self._buffer.get_end_iter()
            )

    def _update_state(self) -> None:
        self._stack.set_visible_child_name("lines" if self._shown_count else "empty")
        self._copy.set_sensitive(bool(self._shown_count))
        self._latest.set_sensitive(not self._at_end)

    # MARK: Scrolling

    def _scrolled(self, adjustment: Gtk.Adjustment) -> None:
        self._at_end = adjustment.get_value() + adjustment.get_page_size() >= adjustment.get_upper() - SCROLL_SLACK
        self._latest.set_sensitive(not self._at_end)

    def _content_grew(self, adjustment: Gtk.Adjustment) -> None:
        if self._at_end:
            self._schedule_follow()

    def _schedule_follow(self) -> None:
        if not self._follow_scheduled:
            self._follow_scheduled = True
            GLib.idle_add(self._follow_newest)

    def _follow_newest(self) -> bool:
        self._follow_scheduled = False
        adjustment = self._adjustment
        adjustment.set_value(adjustment.get_upper() - adjustment.get_page_size())
        self._at_end = True
        return GLib.SOURCE_REMOVE

    def _scroll_to_latest(self) -> None:
        self._at_end = True
        self._schedule_follow()
        self._latest.set_sensitive(False)

    # MARK: Controls

    def _query_changed(self, entry: Gtk.SearchEntry) -> None:
        self._query = entry.get_text()
        self._rebuild()

    def _filter_changed(self, dropdown: Gtk.DropDown, _) -> None:
        self._filter = list(LogFilter)[dropdown.get_selected()]
        self._rebuild()

    def _copy_lines(self) -> None:
        if self._tail is not None:
            self._model.copy("\n".join(entry.plain_text for entry in self._tail.entries if self._visible(entry)))

    def _search_requested(self) -> None:
        """Ctrl+F: the log on screen moves the focus to its search field."""
        request = self._model.search_request
        if request != self._last_search_request:
            self._last_search_request = request
            if self.get_mapped():
                self._search.grab_focus()
