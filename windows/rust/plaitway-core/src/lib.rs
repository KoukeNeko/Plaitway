//! What the UI knows about the daemon: a model of profiles and routes, and a session that keeps it
//! current. The daemon decides who holds a route; nothing here repeats that decision.

mod diag;
mod model;
mod session;
mod store;

pub use diag::*;
pub use model::*;
pub use session::{Link, Session, View};
pub use store::{Language, Placement, Store, Ui as UiPrefs};
pub use plaitway_client::DEFAULT_PIPE;



