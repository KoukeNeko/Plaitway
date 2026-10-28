# Plaitway for Windows

A native Windows app for the Plaitway daemon, written in Rust and drawn with Direct3D 11 (Mica on Windows 11, a solid
colour on Windows 10). It talks to `plaitwayd` over the named pipe with the same gRPC API as the macOS app.

| Directory | Contents |
|---|---|
| `rust` | The app and everything it is made of: see [rust/README.md](rust/README.md) for the crates, how to run it against a fake daemon, and the tests |
| `Resources` | `Windows.xcstrings`: the strings of the Windows app that the macOS catalog does not have; `rust/plaitway-core` reads it with the macOS catalog |
| `installer` | The WiX project of the MSI; `packaging/windows/build-installer.ps1` stages the payload and builds it |
| `global.json` | Pins the .NET SDK that builds the WiX project (the app itself needs no .NET) |

## Build

```powershell
cd windows\rust
cargo build --release
```

The MSI, with the daemon, the command line client, `wintun.dll` and the app: `packaging\windows\build-installer.ps1`.
It needs Go, the .NET SDK of `global.json` (for WiX) and a Rust toolchain. See [../packaging/windows/README.md](../packaging/windows/README.md).
