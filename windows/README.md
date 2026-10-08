# Plaitway for Windows

A native Windows app (C#, .NET 10, WinUI 3, no WebView) for the Plaitway daemon. It mirrors `macos/`: a client
library with no UI dependency, an app on top of it, and tests that run against the real Go daemon.

| Directory | Contents |
|---|---|
| `Sources/Plaitway.Client` | The daemon client library, `net10.0-windows`: the gRPC channel over the named pipe, the check of who serves the pipe, one typed method per RPC, the two watches, and the ports of the pure logic of `macos/Sources/PlaitwayClient` |
| `Sources/Plaitway.AppCore` | The application model and the view models, with no WinUI type: the profile store, the helper setup, the editor, the page and dialog models, the strings. Everything that touches Windows is a port with a fake in the tests |
| `Sources/Plaitway.App` | The WinUI 3 app (unpackaged, no WebView): the window with Mica, the sidebar and the five tabs of a profile, Diagnostics, Settings, the setup screen, the dialogs and the notification-area icon. See [docs/ui-architecture.md](docs/ui-architecture.md) |
| `Tools/Plaitway.Localization` | The build-time tool that turns the macOS catalog and `Resources/Windows.xcstrings` into the `UiText` class and the `.resw` files of the app |
| `Resources/Windows.xcstrings` | The strings that exist only on Windows (the service, the elevation prompt); every other string is the macOS catalog's |
| `Tests/Plaitway.Client.Tests` | xUnit v3 tests: the cases of the Swift and Go tests, and integration tests against `plaitwayd -fake` |
| `Tests/Plaitway.AppCore.Tests` | The ports of the macOS app tests (`AppModelTests`, `ModelTests`, `ProfileEditorTests`, `LocalizationTests`) and the rules of the interface, against the real daemon on its fake backend |
| `Tests/Plaitway.App.UiTests` | Starts the app against `plaitwayd -fake` and drives it through UI Automation: every control has a name, single instance, hide to tray, quit from the tray |
| `scripts` | `capture-ui.ps1` takes screenshots of every page in each theme and language; `Dump-AutomationTree.ps1` writes the UI Automation tree of a running window; `generate-icons.ps1` makes the icons from `Docs/app-icon.png` |
| `docs` | [ui-architecture.md](docs/ui-architecture.md): layers, data flow, conventions, how to add a page, the Swift test mapping. [ui-toolkit.md](docs/ui-toolkit.md): WinUI 3 or WPF, with the evidence |

## Requirements

- Windows 10 1809 or later, x64
- .NET SDK 10 (`global.json` pins 10.0.401, newer feature bands are accepted)
- Go, for the tests (they build `plaitwayd` from this checkout)
- The Windows App Runtime 2.5 for the framework-dependent build of the app; the self-contained publish carries its own

No Visual Studio, workload or global tool is needed. NuGet restores everything.

## Build

```powershell
dotnet build windows\Plaitway.sln -c Release
```

Warnings are errors. Package versions are in `Directory.Packages.props` (central package management).

## Test

```powershell
cd windows
dotnet test --project Tests\Plaitway.Client.Tests -c Release
dotnet test --project Tests\Plaitway.AppCore.Tests -c Release
dotnet test --project Tests\Plaitway.App.UiTests
```

`global.json` selects the Microsoft.Testing.Platform runner, which xUnit v3 needs on the .NET 10 SDK; the project
is given with `--project`.

The tests build `plaitwayd.exe` once into `bin\clienttests-<pid>\` of the repository and delete it afterwards. Every
daemon runs on a pipe and in a state directory of its own, and is placed in a job object that ends it when the test
process ends. The AppCore tests use the same daemon. The UI tests open the app on the screen of whoever runs them, one
window at a time, against a daemon of their own; they never touch the real service, a route or a startup entry.
Environment variables:

| Variable | Effect |
|---|---|
| `PLAITWAY_DAEMON` | Path of a daemon binary to use instead of building one |
| `PLAITWAY_REAL_DAEMON_TESTS=1` | Runs the tests that need the daemon's real engines (the real parsers). They are skipped otherwise: the daemon has real engines on macOS only, and a real daemon would own the machine's routes |

What the tests cover:

- `Config/`: `ConfigTokenizer`, `ConfigDiagnostic` and `SecretMask` with the cases of `ConfigTokenizerTests`,
  `ConfigDiagnosticTests` and `SecretMaskTests` (Swift), the same profiles from `internal/ovpn/testdata`, random
  edits with fixed seeds, and the corpus of `maskSecrets` (Go)
- `Import/`: `ProfileImporter` with `ProfileImporterTests` (Swift) and the inlining tests of `cmd/plaitway` (Go),
  including the Windows spellings, links, junctions and refused network paths
- `Transport/`: the owner rule with the corpus of `internal/transport/serverowner_windows_test.go`; a pipe served by
  the test process with the daemon's access list, an owner chosen by the test, and a seam that poses as another user
  (like `dialOptionsAs` in Go); the exact access mask as the kernel granted it; an ordinary interactive user
  connecting through the Interactive entry only
- `Client/`: every RPC, both watches, their cancellation (the daemon logs `code=Canceled`), a restart of the daemon,
  the credential requests with the fake daemon's `# fake:` markers
- `Storage/`: the in-memory store and the Credential Manager, under a service name of the test's own

Not testable here:

- A refused import for a caller that is not an administrator. On Windows the daemon counts a filtered administrator
  (the Administrators group is deny-only in the token) as an administrator, and the test session is one.
- IME composition for Traditional Chinese. See [docs/ui-toolkit.md](docs/ui-toolkit.md).
- A real Explorer restart, which closes the user's shell. The `TaskbarCreated` handler is tested with the message.

## Run against a fake daemon

```powershell
go build -o $env:TEMP\plaitwayd.exe .\cmd\plaitwayd
go build -o $env:TEMP\plaitway.exe .\cmd\plaitway
$pipe = '\\.\pipe\plaitway-dev'
Start-Process $env:TEMP\plaitwayd.exe -ArgumentList '-fake','-socket',$pipe,'-state-dir',"$env:TEMP\plaitway-state",'-log-file','""','-run-dir',"$env:TEMP\plaitway-run"
& $env:TEMP\plaitway.exe -socket $pipe import .\some.ovpn

dotnet build windows\Sources\Plaitway.App -c Debug
$env:PLAITWAY_SOCKET = $pipe
.\windows\Sources\Plaitway.App\bin\Debug\net10.0-windows10.0.19041.0\win-x64\Plaitway.exe
```

`PLAITWAY_SOCKET` is followed by Debug builds only (`DaemonLocation.OverrideEnabled`). The app answers credential
requests from the Credential Manager, so a release build that followed the variable would hand saved passwords to
whatever process serves the pipe it names. A value that is not `\\.\pipe\<name>` is refused, as the Go command line
client refuses it.

`pwsh scripts\capture-ui.ps1 -ArtifactsDirectory <folder>` does all of this for the light and dark themes and for English
and Traditional Chinese, goes through every page, and leaves a picture and the UI Automation tree of each in the folder.
It leaves nothing running. The window appears on the screen while it runs. Neither it nor the tests run the elevated
helper command that the setup screen's button runs: it is a fake in the tests, and disabled in a build that follows
`PLAITWAY_SOCKET`.

## Application interface

The generated types come from `proto/plaitway/v1/plaitway.proto` and from nowhere else: the client project lists the
file as `<Protobuf Include=... GrpcServices="Client">` and Grpc.Tools runs protoc during every build into `obj/`.
Nothing generated is committed.

This differs from `internal/gen` and `macos/Sources/PlaitwayAPI`, which are committed (`make generate`, buf): the Go
and Swift toolchains cannot run protoc inside their build, so their generated code is a checked-in artifact that has
to be regenerated and reviewed. MSBuild can, so a copy would only go stale.

## Client library

Namespaces of `Plaitway.Client`:

| Namespace | Contents |
|---|---|
| (root) | `DaemonClient`, `ProfileSet`, `ProfileWatchEvent`, `BackoffPolicy`, `DaemonFailure`, `DaemonLocation` |
| `Transport` | `PipePath`, `PipeServerRefusedException`, `PipeUnreachableException`; the dialer and the owner check are internal |
| `Config` | `ConfigTokenizer`, `ConfigDiagnostic`, `SecretMask` |
| `Import` | `ProfileImporter`, `ProfileImportFailure` |
| `Storage` | `ICredentialStore`, `WindowsCredentialStore`, `InMemoryCredentialStore`, `Credentials` |

Calls take a `CancellationToken`, watches are `IAsyncEnumerable`, and nothing keeps static mutable state. Library
errors are typed (`DaemonFailureKind`, `ProfileImportFailure`, exceptions with data); the app words them.

### Connection

The channel is gRPC over HTTP/2 on a `NamedPipeClientStream` made by `SocketsHttpHandler.ConnectCallback`. The pipe
is opened as the Go client opens it (`internal/transport/transport_windows.go`):

- with `PipeAccessRights.ReadData | WriteData | ReadAttributes | ReadPermissions | Synchronize` and nothing else.
  `GENERIC_WRITE` includes `FILE_CREATE_PIPE_INSTANCE`, which the pipe's access list refuses to everyone but SYSTEM,
  Administrators and the daemon's own user.
- at `TokenImpersonationLevel.Identification`, which the daemon needs to read the caller's token.
- and used only after `PipeServerVerifier` has read the owner of the connected pipe instance
  (`GetSecurityInfo`, which needs `READ_CONTROL`) and accepted SYSTEM, `BUILTIN\Administrators` or the calling
  user. Any other owner, or any failure of the check, closes the connection before a byte is sent, with an error
  that starts with `pipe server refused: `.

A pipe that does not exist, or that Windows refuses to this user, is `PipeUnreachableException`. gRPC reports it as
`Unavailable`; `DaemonFailure.From` tells it from a refused server (`ServerRefused`) and from a refusal by Windows
(`PermissionDenied`).

### Watches

`WatchProfilesAsync` and `WatchLogsAsync` do not end when the daemon goes away. They report the outage once
(`DaemonUnavailable`, for the profiles), retry with `BackoffPolicy` (200 ms growing by 1.6 to 5 s, 20 % jitter,
as on macOS) and start again with a snapshot. The log watch skips the lines it has delivered. Cancelling the token,
or leaving the `await foreach`, cancels the call at the daemon; the sequence ends with `OperationCanceledException`.

`ProfileSet` applies the events and keeps the profiles in priority order, with ties in place, as `ProfileStore`
does on macOS.

### Credential Manager

`WindowsCredentialStore` keeps one generic credential per profile and kind, target name
`io.github.koukeneko.plaitway.credentials/<profile id>:<kind>`, with the user name and password as the same JSON
the macOS Keychain item holds. Persistence is `CRED_PERSIST_LOCAL_MACHINE`:

- It survives a sign-out, as `CRED_PERSIST_SESSION` does not, so the user is not asked again after every sign-in.
- It stays on this machine. `CRED_PERSIST_ENTERPRISE` roams with a domain account, and a VPN password belongs to
  this machine's daemon.
- The vault is per user and encrypted with that user's logon secret; another account on the machine cannot read it.

A credential is limited to 2560 bytes; a larger one is refused with `CredentialStoreException`.

### Not ported

- `ProfileStore` and its credential flow (what to answer, when to ask the user): it is the observable model of the
  UI, so it is in `Plaitway.AppCore`, with the tests that are ports of the macOS ones.
- `DaemonInstaller`: `HelperInstaller` in `Plaitway.AppCore` reads the service state from the service control manager
  and, when the user chooses, runs `plaitwayd.exe install -start` elevated.

## Differences from the Swift and Go code

| Case | Swift | Go | C# |
|---|---|---|---|
| Absolute path that leads into the profile's directory | refused | read | read, only with a drive letter |
| `~\x` | outside the directory | a relative name, then missing | outside the directory |
| Path on a network share or a device | n/a | not checked | refused without being opened |
| `; PrivateKey = x` in WireGuard text | shown | hidden | hidden |
| `auth-user-pass <file>` | credentials returned | not handled | credentials returned |

The rows where Swift or Go differ from each other are reported in the task's open issues, not decided here.
