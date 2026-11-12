import Gio from 'gi://Gio';
import GLib from 'gi://GLib';
import GObject from 'gi://GObject';
import St from 'gi://St';
import Clutter from 'gi://Clutter';

import {Extension} from 'resource:///org/gnome/shell/extensions/extension.js';
import * as Main from 'resource:///org/gnome/shell/ui/main.js';
import * as PanelMenu from 'resource:///org/gnome/shell/ui/panelMenu.js';
import * as PopupMenu from 'resource:///org/gnome/shell/ui/popupMenu.js';

import {parseProfiles, summarize, textsFor} from './status.js';

const POLL_SECONDS = 2;
const APP_DESKTOP_ID = 'io.github.koukeneko.Plaitway.desktop';

// Runs the command line client of Plaitway (`plaitway`), which talks to the helper. Resolves with its output.
function plaitway(args) {
    return new Promise((resolve, reject) => {
        let process;
        try {
            process = Gio.Subprocess.new(['plaitway', ...args],
                Gio.SubprocessFlags.STDOUT_PIPE | Gio.SubprocessFlags.STDERR_SILENCE);
        } catch (error) {
            reject(error);
            return;
        }
        process.communicate_utf8_async(null, null, (source, result) => {
            try {
                const [, stdout] = source.communicate_utf8_finish(result);
                if (!source.get_successful())
                    reject(new Error(`plaitway ${args.join(' ')} failed`));
                else
                    resolve(stdout ?? '');
            } catch (error) {
                reject(error);
            }
        });
    });
}

const Indicator = GObject.registerClass(
class PlaitwayIndicator extends PanelMenu.Button {
    _init() {
        super._init(0.5, 'Plaitway');
        this._texts = textsFor(GLib.get_language_names());
        this._profiles = [];
        this._available = false;
        this._shown = null;

        const box = new St.BoxLayout({style_class: 'panel-status-menu-box'});
        this._icon = new St.Icon({icon_name: 'plaitway-state-idle-symbolic', style_class: 'system-status-icon'});
        this._count = new St.Label({y_align: Clutter.ActorAlign.CENTER, visible: false});
        box.add_child(this._icon);
        box.add_child(this._count);
        this.add_child(box);

        this._timeout = GLib.timeout_add_seconds(GLib.PRIORITY_DEFAULT, POLL_SECONDS, () => {
            this._refresh();
            return GLib.SOURCE_CONTINUE;
        });
        this._refresh();
    }

    async _refresh() {
        let text = '';
        try {
            text = await plaitway(['status', '-json']);
            this._available = true;
        } catch (error) {
            this._available = false;
        }
        // A callback that arrives after destroy() has nothing to draw on.
        if (!this._timeout)
            return;
        let profiles = [];
        if (this._available) {
            try {
                profiles = parseProfiles(text);
            } catch (error) {
                this._available = false;
            }
        }
        const key = JSON.stringify([this._available, profiles]);
        if (key === this._shown)
            return;
        this._shown = key;
        this._profiles = profiles;
        this._draw();
    }

    _draw() {
        const summary = summarize(this._profiles, this._texts);
        this._icon.icon_name = this._available ? summary.icon : 'plaitway-state-attention-symbolic';
        this._count.text = summary.count;
        this._count.visible = this._available && summary.count !== '';
        this._words = this._available ? summary.words : this._texts.unavailable;

        const menu = this.menu;
        menu.removeAll();
        menu.addMenuItem(new PopupMenu.PopupMenuItem(this._words, {reactive: false, can_focus: false}));
        if (this._available && this._profiles.length > 0) {
            menu.addMenuItem(new PopupMenu.PopupSeparatorMenuItem());
            for (const profile of this._profiles) {
                const item = new PopupMenu.PopupSwitchMenuItem(profile.name, profile.enabled);
                item.connect('toggled', (_, active) => this._switch(profile, active));
                menu.addMenuItem(item);
            }
            menu.addMenuItem(new PopupMenu.PopupSeparatorMenuItem());
            const off = new PopupMenu.PopupMenuItem(this._texts.disconnectAll);
            off.setSensitive(this._profiles.some(profile => profile.enabled));
            off.connect('activate', () => this._disconnectAll());
            menu.addMenuItem(off);
        }
        menu.addMenuItem(new PopupMenu.PopupSeparatorMenuItem());
        const open = new PopupMenu.PopupMenuItem(this._texts.open);
        open.connect('activate', () => Gio.DesktopAppInfo.new(APP_DESKTOP_ID)?.launch([], null));
        menu.addMenuItem(open);
    }

    // `connect` waits until the tunnel is up, which can take long: the next poll shows how it went.
    _switch(profile, active) {
        plaitway([active ? 'connect' : 'disconnect', profile.id]).catch(error => console.error(`Plaitway: ${error.message}`));
    }

    _disconnectAll() {
        for (const profile of this._profiles.filter(p => p.enabled))
            this._switch(profile, false);
    }

    destroy() {
        if (this._timeout) {
            GLib.source_remove(this._timeout);
            this._timeout = 0;
        }
        super.destroy();
    }
});

export default class PlaitwayExtension extends Extension {
    enable() {
        this._indicator = new Indicator();
        Main.panel.addToStatusArea(this.uuid, this._indicator);
    }

    disable() {
        this._indicator?.destroy();
        this._indicator = null;
    }
}
