//! What the UI knows about the daemon: a model of profiles and routes, and a session that keeps it
//! current. The daemon decides who holds a route; nothing here repeats that decision.

mod model;
mod session;

pub use model::*;
pub use session::{Link, Session, View};
pub use plaitway_client::DEFAULT_PIPE;

