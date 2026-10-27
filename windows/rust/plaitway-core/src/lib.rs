//! What the UI knows about the daemon: a model of profiles and routes, and a session that keeps it
//! current. The daemon decides who holds a route; nothing here repeats that decision.

mod diag;
mod i18n;
mod model;
mod session;
mod store;

pub use diag::*;
pub use i18n::{language, set_language, tr, tr_fmt, Language as Lang};
pub use model::*;
pub use session::{ContentView, Link, Session, View};
pub use store::{Language, Placement, Store, Ui as UiPrefs};
pub use plaitway_client::DEFAULT_PIPE;





