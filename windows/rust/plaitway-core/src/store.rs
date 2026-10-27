//! What the app remembers between runs, in `%LOCALAPPDATA%\Plaitway`. A missing or damaged file means
//! defaults, never a refusal to start; a write goes to a temporary file first so a crash cannot leave
//! half a file behind.

use std::{
    fs, io,
    path::{Path, PathBuf},
};

use serde::{Deserialize, Serialize};

/// The language, in the file the WinUI app already writes (`settings.json`, `{"Language": "..."}`).
#[derive(Serialize, Deserialize, Default, Clone, PartialEq, Debug)]
pub struct Language {
    #[serde(rename = "Language")]
    pub language: Option<String>,
}

/// Where the window was, in the units `GetWindowPlacement` uses.
#[derive(Serialize, Deserialize, Clone, Copy, PartialEq, Debug)]
pub struct Placement {
    pub x: i32,
    pub y: i32,
    pub w: i32,
    pub h: i32,
    pub maximized: bool,
}

/// The rest of what the app remembers (`ui.json`).
#[derive(Serialize, Deserialize, Clone, PartialEq, Debug)]
#[serde(default)]
pub struct Ui {
    /// Closing the window keeps the app in the notification area.
    pub close_to_tray: bool,
    pub window: Option<Placement>,
}

impl Default for Ui {
    fn default() -> Self {
        Ui { close_to_tray: true, window: None }
    }
}

pub struct Store {
    dir: PathBuf,
}

impl Store {
    pub fn new(dir: impl Into<PathBuf>) -> Store {
        Store { dir: dir.into() }
    }

    /// `%LOCALAPPDATA%\Plaitway`, or the temporary directory when that is not set.
    pub fn default_dir() -> PathBuf {
        std::env::var_os("LOCALAPPDATA").map(PathBuf::from).unwrap_or_else(std::env::temp_dir).join("Plaitway")
    }

    fn read<T: for<'a> Deserialize<'a> + Default>(&self, file: &str) -> T {
        fs::read_to_string(self.dir.join(file)).ok().and_then(|text| serde_json::from_str(&text).ok()).unwrap_or_default()
    }

    fn write<T: Serialize>(&self, file: &str, value: &T) -> io::Result<()> {
        fs::create_dir_all(&self.dir)?;
        let (path, tmp) = (self.dir.join(file), self.dir.join(format!("{file}.tmp")));
        fs::write(&tmp, serde_json::to_vec_pretty(value).map_err(io::Error::other)?)?;
        replace(&tmp, &path)
    }

    pub fn language(&self) -> Language {
        self.read("settings.json")
    }

    pub fn save_language(&self, l: &Language) -> io::Result<()> {
        self.write("settings.json", l)
    }

    pub fn ui(&self) -> Ui {
        self.read("ui.json")
    }

    pub fn save_ui(&self, u: &Ui) -> io::Result<()> {
        self.write("ui.json", u)
    }
}

/// Move `from` over `to`; `rename` does that on Windows when `to` exists.
fn replace(from: &Path, to: &Path) -> io::Result<()> {
    fs::rename(from, to)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn dir(name: &str) -> PathBuf {
        let d = std::env::temp_dir().join(format!("plaitway-store-{}-{name}", std::process::id()));
        let _ = fs::remove_dir_all(&d);
        d
    }

    #[test]
    fn nothing_saved_means_defaults() {
        let s = Store::new(dir("empty"));
        assert_eq!(s.ui(), Ui::default());
        assert!(s.ui().close_to_tray);
        assert_eq!(s.language(), Language::default());
    }

    #[test]
    fn what_is_saved_comes_back() {
        let s = Store::new(dir("roundtrip"));
        let u = Ui { close_to_tray: false, window: Some(Placement { x: 10, y: 20, w: 1280, h: 800, maximized: true }) };
        s.save_ui(&u).unwrap();
        assert_eq!(s.ui(), u);
        s.save_language(&Language { language: Some("zh-TW".into()) }).unwrap();
        assert_eq!(s.language().language.as_deref(), Some("zh-TW"));
        // Saving again replaces the file.
        s.save_ui(&Ui::default()).unwrap();
        assert_eq!(s.ui(), Ui::default());
    }

    #[test]
    fn a_damaged_file_is_ignored() {
        let d = dir("damaged");
        fs::create_dir_all(&d).unwrap();
        fs::write(d.join("ui.json"), "{ not json").unwrap();
        fs::write(d.join("settings.json"), "").unwrap();
        let s = Store::new(&d);
        assert_eq!(s.ui(), Ui::default());
        assert_eq!(s.language(), Language::default());
    }

    #[test]
    fn missing_fields_take_their_defaults() {
        let d = dir("partial");
        fs::create_dir_all(&d).unwrap();
        fs::write(d.join("ui.json"), r#"{"window":{"x":1,"y":2,"w":3,"h":4,"maximized":false}}"#).unwrap();
        let s = Store::new(&d);
        assert!(s.ui().close_to_tray, "not mentioned, so the default");
        assert_eq!(s.ui().window.unwrap().w, 3);
    }

    #[test]
    fn the_language_file_is_the_one_the_winui_app_writes() {
        let d = dir("compat");
        fs::create_dir_all(&d).unwrap();
        fs::write(d.join("settings.json"), r#"{"Language":"ja"}"#).unwrap();
        assert_eq!(Store::new(&d).language().language.as_deref(), Some("ja"));
        let s = Store::new(&d);
        s.save_language(&Language { language: Some("en".into()) }).unwrap();
        assert!(fs::read_to_string(d.join("settings.json")).unwrap().contains("\"Language\""));
    }
}
