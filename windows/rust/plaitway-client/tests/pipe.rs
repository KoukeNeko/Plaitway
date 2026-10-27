//! The client against an in-process daemon on a real named pipe.

use std::{
    io,
    pin::Pin,
    task::{Context, Poll},
};

use plaitway_client::{connect, pb::*};
use tokio::{
    io::{AsyncRead, AsyncWrite, ReadBuf},
    net::windows::named_pipe::{NamedPipeServer, ServerOptions},
};
use tokio_stream::{Empty, Iter};
use tonic::{transport::server::Connected, transport::Server, Request, Response, Status};

struct Conn(NamedPipeServer);

impl Connected for Conn {
    type ConnectInfo = ();
    fn connect_info(&self) -> Self::ConnectInfo {}
}

impl AsyncRead for Conn {
    fn poll_read(mut self: Pin<&mut Self>, cx: &mut Context<'_>, buf: &mut ReadBuf<'_>) -> Poll<io::Result<()>> {
        Pin::new(&mut self.0).poll_read(cx, buf)
    }
}

impl AsyncWrite for Conn {
    fn poll_write(mut self: Pin<&mut Self>, cx: &mut Context<'_>, buf: &[u8]) -> Poll<io::Result<usize>> {
        Pin::new(&mut self.0).poll_write(cx, buf)
    }
    fn poll_flush(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Pin::new(&mut self.0).poll_flush(cx)
    }
    fn poll_shutdown(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Pin::new(&mut self.0).poll_shutdown(cx)
    }
}

type Rsp<T> = Result<Response<T>, Status>;

fn no<T>() -> Rsp<T> {
    Err(Status::unimplemented("not in this test"))
}

struct Mock;

#[tonic::async_trait]
impl daemon_service_server::DaemonService for Mock {
    async fn get_daemon_info(&self, _: Request<GetDaemonInfoRequest>) -> Rsp<DaemonInfo> {
        Ok(Response::new(DaemonInfo { version: "0.6.1".into(), protocol_version: 7, privileged: true, engines: vec![] }))
    }
    async fn get_diagnostics(&self, _: Request<GetDiagnosticsRequest>) -> Rsp<Diagnostics> {
        no()
    }
    async fn list_profiles(&self, _: Request<ListProfilesRequest>) -> Rsp<ListProfilesResponse> {
        Ok(Response::new(ListProfilesResponse { profiles: vec![Profile { id: "p1".into(), name: "Home".into(), ..Default::default() }] }))
    }
    async fn import_profile(&self, _: Request<ImportProfileRequest>) -> Rsp<ImportProfileResponse> {
        no()
    }
    async fn update_profile(&self, _: Request<UpdateProfileRequest>) -> Rsp<Profile> {
        no()
    }
    async fn get_profile_content(&self, _: Request<GetProfileContentRequest>) -> Rsp<GetProfileContentResponse> {
        no()
    }
    async fn update_profile_content(&self, _: Request<UpdateProfileContentRequest>) -> Rsp<ImportProfileResponse> {
        no()
    }
    async fn delete_profile(&self, _: Request<DeleteProfileRequest>) -> Rsp<DeleteProfileResponse> {
        no()
    }
    async fn reorder_profiles(&self, _: Request<ReorderProfilesRequest>) -> Rsp<ListProfilesResponse> {
        no()
    }
    async fn set_profile_enabled(&self, r: Request<SetProfileEnabledRequest>) -> Rsp<Profile> {
        Ok(Response::new(Profile { id: r.into_inner().id, desired_enabled: true, ..Default::default() }))
    }
    async fn provide_credentials(&self, _: Request<ProvideCredentialsRequest>) -> Rsp<Profile> {
        no()
    }
    type WatchProfilesStream = Empty<Result<ProfileEvent, Status>>;
    async fn watch_profiles(&self, _: Request<WatchProfilesRequest>) -> Rsp<Self::WatchProfilesStream> {
        Ok(Response::new(tokio_stream::empty()))
    }
    type WatchLogsStream = Iter<std::vec::IntoIter<Result<LogLine, Status>>>;
    async fn watch_logs(&self, _: Request<WatchLogsRequest>) -> Rsp<Self::WatchLogsStream> {
        Ok(Response::new(tokio_stream::iter(vec![Ok(LogLine::default()), Ok(LogLine::default()), Ok(LogLine::default())])))
    }
    async fn resync(&self, _: Request<ResyncRequest>) -> Rsp<ResyncResponse> {
        no()
    }
    async fn remove_stale_route(&self, _: Request<RemoveStaleRouteRequest>) -> Rsp<RemoveStaleRouteResponse> {
        no()
    }
}

fn serve(name: String) {
    let incoming = async_stream::stream! {
        let mut server = ServerOptions::new().first_pipe_instance(true).create(&name).unwrap();
        loop {
            server.connect().await.unwrap();
            let conn = Conn(server);
            server = ServerOptions::new().create(&name).unwrap();
            yield Ok::<_, io::Error>(conn);
        }
    };
    tokio::spawn(Server::builder().add_service(daemon_service_server::DaemonServiceServer::new(Mock)).serve_with_incoming(incoming));
}

fn pipe(test: &str) -> String {
    format!(r"\\.\pipe\plaitway-client-test-{}-{test}", std::process::id())
}

#[tokio::test(flavor = "multi_thread")]
async fn calls_and_streams_work_over_a_named_pipe() {
    let name = pipe("calls");
    serve(name.clone());
    tokio::time::sleep(std::time::Duration::from_millis(100)).await;

    let mut c = connect(&name).await.expect("connect");
    let info = c.get_daemon_info(GetDaemonInfoRequest {}).await.unwrap().into_inner();
    assert_eq!((info.version.as_str(), info.protocol_version, info.privileged), ("0.6.1", 7, true));

    let list = c.list_profiles(ListProfilesRequest {}).await.unwrap().into_inner();
    assert_eq!(list.profiles[0].name, "Home");

    let enabled = c.set_profile_enabled(SetProfileEnabledRequest { id: "p9".into(), enabled: true }).await.unwrap().into_inner();
    assert_eq!(enabled.id, "p9");

    let mut logs = c.watch_logs(WatchLogsRequest::default()).await.unwrap().into_inner();
    let mut n = 0;
    while logs.message().await.unwrap().is_some() {
        n += 1;
    }
    assert_eq!(n, 3, "the whole stream arrives, then it ends");
}

#[tokio::test(flavor = "multi_thread")]
async fn a_failing_call_reports_the_daemons_status() {
    let name = pipe("errors");
    serve(name.clone());
    tokio::time::sleep(std::time::Duration::from_millis(100)).await;
    let mut c = connect(&name).await.unwrap();
    let err = c.resync(ResyncRequest::default()).await.unwrap_err();
    assert_eq!(err.code(), tonic::Code::Unimplemented);
}

#[tokio::test]
async fn connecting_to_a_missing_pipe_fails_and_a_remote_one_is_refused() {
    assert!(connect(&pipe("nobody-listens")).await.is_err());
    let err = connect(r"\\server\pipe\plaitway").await.unwrap_err();
    assert!(err.to_string().contains("not a named pipe"));
}
