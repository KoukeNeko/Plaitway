//! A client for `plaitwayd`: the generated gRPC service, reached over a Windows named pipe.

use std::{io, time::Duration};

use hyper_util::rt::TokioIo;
use tokio::net::windows::named_pipe::ClientOptions;
use tonic::transport::{Endpoint, Uri};
use tower::service_fn;

#[allow(clippy::all)]
pub mod pb {
    tonic::include_proto!("plaitway.v1");
}

pub use pb::daemon_service_client::DaemonServiceClient;
pub use tonic::transport::Channel;

/// The pipe the daemon listens on unless told otherwise.
pub const DEFAULT_PIPE: &str = r"\\.\pipe\plaitway";

const ERROR_PIPE_BUSY: i32 = 231;

/// Only `\\.\pipe\<name>` is a pipe; anything else (a remote `\\server\pipe\x`, a bare path) is refused
/// so a mistyped address can never reach another machine.
pub fn validate_pipe(path: &str) -> Result<&str, String> {
    match path.strip_prefix(r"\\.\pipe\") {
        Some(name) if !name.is_empty() && !name.contains('\\') => Ok(path),
        _ => Err(format!(r"{path} is not a named pipe, use \\.\pipe\<name>")),
    }
}

async fn open(path: &str) -> io::Result<tokio::net::windows::named_pipe::NamedPipeClient> {
    // A busy pipe means every instance is serving someone; the daemon makes a new one within a moment.
    for _ in 0..20 {
        match ClientOptions::new().open(path) {
            Err(e) if e.raw_os_error() == Some(ERROR_PIPE_BUSY) => tokio::time::sleep(Duration::from_millis(50)).await,
            r => return r,
        }
    }
    ClientOptions::new().open(path)
}

/// Connect to the daemon on `pipe` and return a ready client.
pub async fn connect(pipe: &str) -> Result<DaemonServiceClient<Channel>, Box<dyn std::error::Error + Send + Sync>> {
    let path = validate_pipe(pipe)?.to_string();
    // The URI is never dialled; tonic needs one to build the channel.
    let channel = Endpoint::try_from("http://plaitway.invalid")?
        .connect_with_connector(service_fn(move |_: Uri| {
            let path = path.clone();
            async move { Ok::<_, io::Error>(TokioIo::new(open(&path).await?)) }
        }))
        .await?;
    Ok(DaemonServiceClient::new(channel))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn only_local_pipes_are_accepted() {
        assert!(validate_pipe(DEFAULT_PIPE).is_ok());
        assert!(validate_pipe(r"\\.\pipe\plaitway-test-1-2").is_ok());
        for bad in [r"\\.\pipe\", r"\\.\pipe", r"\\server\pipe\plaitway", r"\\.\pipe\a\b", "plaitway", ""] {
            assert!(validate_pipe(bad).is_err(), "{bad} must be refused");
        }
    }
}


