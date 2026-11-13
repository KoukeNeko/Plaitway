// gjs -m linux/tests/gnome_status_test.js : the logic of the GNOME Shell extension's top bar item.
import {badge, parseProfiles, summarize, textsFor} from '../data/share/gnome-shell/extensions/plaitway@koukeneko.github.io/status.js';

function check(condition, what) {
    if (!condition) {
        printerr(`FAIL: ${what}`);
        imports.system.exit(1);
    }
}

const en = textsFor(['en_US', 'en']);
const zh = textsFor(['zh_TW.UTF-8', 'zh_TW', 'zh']);
check(en.open === 'Open Plaitway' && zh.open === '打開 Plaitway', 'the language follows the session');
check(textsFor(['zh_CN', 'zh']).open === 'Open Plaitway', 'Simplified Chinese reads as English, as in the app');

const profile = (name, state, desiredEnabled = true) => ({id: name, name, state: `PROFILE_STATE_${state}`, desiredEnabled, lastError: ''});
const answer = profiles => JSON.stringify({profiles});

let profiles = parseProfiles(answer([profile('home', 'CONNECTED'), profile('office', 'DISCONNECTED', false)]));
check(profiles.length === 2 && profiles[0].state === 'connected' && profiles[1].state === 'idle', 'the states are read');
let summary = summarize(profiles, en);
check(summary.icon === 'plaitway-state-connected-symbolic' && summary.count === '1' && summary.words === 'Connected: 1', 'one connected');

summary = summarize(parseProfiles(answer([profile('home', 'CONNECTED'), profile('lab', 'FAILED')])), en);
check(summary.icon === 'plaitway-state-attention-symbolic' && summary.words === 'Problem' && summary.count === '1', 'a failure comes before a connection');

summary = summarize(parseProfiles(answer([profile('home', 'CONNECTING')])), zh);
check(summary.icon === 'plaitway-state-connecting-symbolic' && summary.words === '連線中' && summary.count === '', 'connecting');

summary = summarize(parseProfiles(answer([])), en);
check(summary.icon === 'plaitway-state-idle-symbolic' && summary.words === 'Not connected' && summary.count === '', 'nothing');
check(parseProfiles('{}').length === 0, 'an answer with no profiles');
check(badge('connecting').spinner, 'a spinner while connecting');
check(badge('connected').icon === 'object-select-symbolic' && !badge('connected').spinner, 'a check when connected');
check(badge('failed').icon === 'window-close-symbolic' && badge('failed').style === 'error', 'a cross when it failed');
check(badge('idle').style === 'dim-label' && badge('nonsense').icon === 'plaitway-state-idle-symbolic', 'a dim dot otherwise');
print('ok');
