// What the top bar item shows, from the JSON of `plaitway status -json`. No Shell imports: gjs runs
// linux/tests/gnome_status_test.js against it.

const STATES = {
    PROFILE_STATE_CONNECTED: 'connected',
    PROFILE_STATE_CONNECTING: 'connecting',
    PROFILE_STATE_RECONNECTING: 'connecting',
    PROFILE_STATE_DISCONNECTING: 'connecting',
    PROFILE_STATE_AWAITING_CREDENTIALS: 'credentials',
    PROFILE_STATE_FAILED: 'failed',
};

const TEXTS = {
    en: {
        connectedCount: n => `Connected: ${n}`,
        notConnected: 'Not connected',
        connecting: 'Connecting',
        problem: 'Problem',
        unavailable: 'Helper unavailable',
        open: 'Open Plaitway',
        disconnectAll: 'Disconnect All',
    },
    'zh-Hant': {
        connectedCount: n => `已連線：${n}`,
        notConnected: '未連線',
        connecting: '連線中',
        problem: '發生問題',
        unavailable: '輔助程式無法使用',
        open: '打開 Plaitway',
        disconnectAll: '全部中斷連線',
    },
};

// The words of the app, in the language of the session: Traditional Chinese for zh-TW, zh-HK, zh-MO and zh-Hant.
export function textsFor(languageNames) {
    const zh = languageNames.some(name => /^zh[_-](TW|HK|MO|Hant)/i.test(name));
    return TEXTS[zh ? 'zh-Hant' : 'en'];
}

export function parseProfiles(text) {
    const answer = JSON.parse(text);
    return (answer.profiles ?? []).map(profile => ({
        id: profile.id,
        name: profile.name,
        state: STATES[profile.state] ?? 'idle',
        enabled: profile.desiredEnabled === true,
        error: profile.lastError ?? '',
    }));
}

// What the icon, the number beside it and the tooltip say about all profiles together. What needs the
// person comes before what is under way, as in the app's tray (core/aggregate.py).
export function summarize(profiles, texts) {
    const states = new Set(profiles.map(profile => profile.state));
    const connected = profiles.filter(profile => profile.state === 'connected').length;
    let icon = 'plaitway-state-idle-symbolic';
    let words = texts.notConnected;
    if (states.has('failed')) {
        icon = 'plaitway-state-attention-symbolic';
        words = texts.problem;
    } else if (states.has('credentials')) {
        icon = 'plaitway-state-attention-symbolic';
        words = texts.problem;
    } else if (states.has('connecting')) {
        icon = 'plaitway-state-connecting-symbolic';
        words = connected > 0 ? texts.connectedCount(connected) : texts.connecting;
    } else if (connected > 0) {
        icon = 'plaitway-state-connected-symbolic';
        words = texts.connectedCount(connected);
    }
    return {icon, connected, words, count: connected > 0 ? String(connected) : ''};
}
