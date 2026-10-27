//! Words in the user's language. English is the key: a text with no translation is shown as it is.
//! The translations are the ones of the shared string catalogs (`macos/.../Localizable.xcstrings` and
//! `windows/Resources/Windows.xcstrings`), so a word means the same in every app of Plaitway, plus the
//! few the Windows UI needs and the catalogs do not have (`EXTRA`).

use std::{
    borrow::Cow,
    collections::HashMap,
    sync::{
        atomic::{AtomicU8, Ordering},
        OnceLock,
    },
};

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Language {
    English,
    /// Traditional Chinese, as the catalogs write it (`zh-Hant`).
    TraditionalChinese,
}

impl Language {
    /// The code stored in `settings.json`.
    pub fn code(self) -> &'static str {
        match self {
            Language::English => "en",
            Language::TraditionalChinese => "zh-Hant",
        }
    }

    pub fn from_code(code: &str) -> Option<Language> {
        match code {
            "en" => Some(Language::English),
            "zh-Hant" => Some(Language::TraditionalChinese),
            _ => None,
        }
    }

    /// What Windows is set to: `zh-TW`, `zh-HK`, `zh-MO` and `zh-Hant*` are Traditional Chinese.
    pub fn from_locale(name: &str) -> Language {
        let n = name.to_ascii_lowercase();
        if n.starts_with("zh-tw") || n.starts_with("zh-hk") || n.starts_with("zh-mo") || n.starts_with("zh-hant") {
            Language::TraditionalChinese
        } else {
            Language::English
        }
    }
}

static CURRENT: AtomicU8 = AtomicU8::new(0);

pub fn set_language(l: Language) {
    CURRENT.store(l as u8, Ordering::Relaxed);
}

pub fn language() -> Language {
    if CURRENT.load(Ordering::Relaxed) == 1 {
        Language::TraditionalChinese
    } else {
        Language::English
    }
}

/// English text and its Traditional Chinese, for what the catalogs do not have. `{0}`, `{1}` are filled by `tr_fmt`.
const EXTRA: &[(&str, &str)] = &[
    ("Routing", "路由"),
    ("Waiting for Windows to ask for consent", "等待 Windows 要求確認"),
    ("Helper not found", "找不到 helper"),
    ("Helper not installed", "尚未安裝 helper"),
    ("Helper state unknown", "無法得知 helper 的狀態"),
    ("Install the helper", "安裝 helper"),
    ("Update the helper", "更新 helper"),
    ("Uninstall the helper", "解除安裝 helper"),
    ("Helper", "Helper"),
    ("Not installed", "未安裝"),
    ("Traffic", "流量"),
    ("none", "無"),
    ("None", "無"),
    ("On", "開"),
    ("Off", "關"),
    ("PROFILES", "設定檔"),
    ("Show Plaitway", "顯示 Plaitway"),
    ("Language", "語言"),
    ("System default", "系統預設"),
    ("English", "English"),
    ("Traditional Chinese", "繁體中文"),
    ("Start with Windows", "隨 Windows 啟動"),
    ("Keep running in the tray when closed", "關閉視窗後繼續在通知區域執行"),
    ("Connect when Plaitway starts", "Plaitway 啟動時連線"),
    ("Keep the local network out of the tunnel", "不讓區域網路經過通道"),
    ("Traffic through the tunnel", "經過通道的流量"),
    ("As the profile says", "依設定檔"),
    ("All traffic", "所有流量"),
    ("Only its routes", "只走它的路由"),
    ("Import a profile", "匯入設定檔"),
    ("No profiles yet", "還沒有設定檔"),
    ("Import a WireGuard or OpenVPN profile to see its routes here.", "匯入 WireGuard 或 OpenVPN 設定檔,就能在這裡看到它的路由。"),
    ("or drop a .conf or .ovpn file here", "或把 .conf、.ovpn 檔案拖到這裡"),
    ("plaitwayd is not running", "plaitwayd 沒有執行"),
    ("Trying again every second.", "每秒重試一次。"),
    ("Connecting to the daemon", "正在連線到背景服務"),
    ("No route is asked for yet", "還沒有路由被要求"),
    ("All internet traffic", "所有網際網路流量"),
    ("Name lookups", "名稱查詢"),
    ("Delete this profile for good?", "要永久刪除這個設定檔嗎?"),
    ("Save and reconnect", "儲存並重新連線"),
    ("Ctrl+S saves.", "按 Ctrl+S 儲存。"),
    ("Loading the profile.", "正在載入設定檔。"),
    ("Routes", "路由"),
    ("Not connected, so nothing is asked for.", "尚未連線,所以沒有要求任何路由。"),
    ("The configuration editor arrives in a later step.", "組態編輯器之後才會提供。"),
    ("This profile needs a username and password", "這個設定檔需要使用者名稱與密碼"),
    ("This profile needs the passphrase of its key", "這個設定檔需要金鑰的通關密語"),
    ("Needs credentials", "需要登入資訊"),
    ("Not connected", "未連線"),
    ("Protocol", "通訊協定"),
    ("Endpoint", "端點"),
    ("Address", "位址"),
    ("Routes it holds", "它持有的路由"),
    ("Uptime", "連線時間"),
    ("No routes held", "未持有路由"),
    ("What the daemon owns on this machine", "背景服務在這台電腦上管理的內容"),
    ("Daemon", "背景服務"),
    ("Running", "執行中"),
    ("Connecting", "連線中"),
    ("Version", "版本"),
    ("Default gateway", "預設閘道"),
    ("Network changed", "網路變更"),
    ("Journal", "紀錄簿"),
    ("Daemon log", "背景服務記錄"),
    ("None. Everything on this machine is accounted for.", "沒有。這台電腦上的一切都有交代。"),
    ("No log lines yet.", "還沒有記錄。"),
    ("Nothing has changed yet.", "還沒有任何變更。"),
    ("Profile not found", "找不到設定檔"),
    ("It may have been deleted", "它可能已被刪除"),
    ("Quit", "結束"),
    ("{0} of {1} routes are held by a tunnel", "{1} 條路由中有 {0} 條由通道持有"),
    ("Holds {0} of {1} routes", "持有 {0}/{1} 條路由"),
    ("Priority {0} · Holds {1} of {2} routes", "優先順序 {0} · 持有 {1}/{2} 條路由"),
    ("Priority {0} · Not connected", "優先順序 {0} · 未連線"),
    ("Priority {0} · Connecting", "優先順序 {0} · 連線中"),
    ("Priority {0} · Disconnecting", "優先順序 {0} · 中斷連線中"),
    ("Priority {0} · Needs credentials", "優先順序 {0} · 需要登入資訊"),
    ("Priority {0} · Failed", "優先順序 {0} · 失敗"),
    ("{0} · priority {1}", "{0} · 優先順序 {1}"),
    ("Asked by {0}", "由 {0} 要求"),
    ("{0} holds", "{0} 持有"),
    ("{0} waits", "{0} 等待"),
    ("{0}: {1} holds it", "{0}:由 {1} 持有"),
    ("{0}: {1} holds it, {2} waits behind", "{0}:由 {1} 持有,{2} 在後面等待"),
    ("{0}: no tunnel holds it", "{0}:沒有通道持有它"),
    ("Routes the daemon owns ({0})", "背景服務管理的路由({0})"),
    ("Stale routes ({0})", "殘留的路由({0})"),
    ("{0} · {1}", "{0} · {1}"),
];

fn catalog() -> &'static HashMap<String, String> {
    static TABLE: OnceLock<HashMap<String, String>> = OnceLock::new();
    TABLE.get_or_init(|| {
        let mut table = HashMap::new();
        for text in [include_str!("../../../../macos/Sources/PlaitwayMenuBar/Resources/Localizable.xcstrings"), include_str!("../../../Resources/Windows.xcstrings")] {
            let Ok(json) = serde_json::from_str::<serde_json::Value>(text) else { continue };
            let Some(strings) = json.get("strings").and_then(|s| s.as_object()) else { continue };
            for (key, entry) in strings {
                // Entries with plural variations have no single value; they are left to the English.
                if let Some(v) = entry.pointer("/localizations/zh-Hant/stringUnit/value").and_then(|v| v.as_str()) {
                    table.insert(key.clone(), v.to_string());
                }
            }
        }
        // Ours come last: where both have a word, the Windows UI's own wins.
        for (en, zh) in EXTRA {
            table.insert((*en).to_string(), (*zh).to_string());
        }
        table
    })
}

/// `key` in the language that is set, or `key` itself.
pub fn tr(key: &str) -> Cow<'_, str> {
    if language() == Language::English {
        return Cow::Borrowed(key);
    }
    match catalog().get(key) {
        Some(t) => Cow::Owned(t.clone()),
        None => Cow::Borrowed(key),
    }
}

/// `key` translated, with `{0}`, `{1}` ... replaced by `args`.
pub fn tr_fmt(key: &str, args: &[&str]) -> String {
    let mut text = tr(key).into_owned();
    for (i, a) in args.iter().enumerate() {
        text = text.replace(&format!("{{{i}}}"), a);
    }
    text
}

#[cfg(test)]
mod tests {
    use super::*;

    /// The language is a global; tests that change it run one after another.
    static LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());

    #[test]
    fn english_is_the_key_itself() {
        let _g = LOCK.lock().unwrap();
        set_language(Language::English);
        assert_eq!(tr("Disconnect"), "Disconnect");
        assert_eq!(tr("A sentence nobody translated"), "A sentence nobody translated");
        assert_eq!(tr_fmt("{0} of {1} routes are held by a tunnel", &["3", "5"]), "3 of 5 routes are held by a tunnel");
    }

    #[test]
    fn chinese_comes_from_the_shared_catalogs_and_ours() {
        let _g = LOCK.lock().unwrap();
        set_language(Language::TraditionalChinese);
        assert_eq!(tr("Disconnect"), "中斷連線", "from the macOS catalog");
        assert_eq!(tr("Settings"), "設定");
        assert_eq!(tr("Routing"), "路由", "from ours");
        assert_eq!(tr("A sentence nobody translated"), "A sentence nobody translated", "no translation: shown in English");
        assert_eq!(tr_fmt("Holds {0} of {1} routes", &["3", "5"]), "持有 3/5 條路由");
        assert_eq!(tr_fmt("Asked by {0}", &["Home, Office"]), "由 Home, Office 要求");
        set_language(Language::English);
    }

    #[test]
    fn every_translation_of_ours_keeps_the_same_placeholders() {
        for (en, zh) in EXTRA {
            for i in 0..4 {
                let p = format!("{{{i}}}");
                assert_eq!(en.contains(&p), zh.contains(&p), "{en:?} and {zh:?} disagree about {p}");
            }
            assert!(!zh.is_empty());
        }
    }

    #[test]
    fn the_system_language_picks_traditional_chinese_for_the_places_that_write_it() {
        for n in ["zh-TW", "zh-Hant-TW", "zh-HK", "zh-MO", "ZH-tw"] {
            assert_eq!(Language::from_locale(n), Language::TraditionalChinese, "{n}");
        }
        for n in ["en-US", "zh-CN", "zh-Hans", "ja-JP", ""] {
            assert_eq!(Language::from_locale(n), Language::English, "{n}");
        }
    }

    #[test]
    fn codes_round_trip() {
        for l in [Language::English, Language::TraditionalChinese] {
            assert_eq!(Language::from_code(l.code()), Some(l));
        }
        assert_eq!(Language::from_code("fr"), None);
    }

    #[test]
    fn the_catalogs_were_found_and_have_what_the_ui_uses() {
        assert!(catalog().len() > 150, "{} entries", catalog().len());
        for k in ["Disconnect", "Connect", "Diagnostics", "Overview", "Logs", "Configuration", "Cancel", "Delete", "Save"] {
            assert!(catalog().contains_key(k), "{k}");
        }
    }
}




