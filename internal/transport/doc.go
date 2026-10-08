// Package transport hides the per-OS local IPC mechanism (Unix domain socket
// here, named pipe on Windows) behind four functions.
//
// On Windows the client also verifies who serves the pipe before it sends
// anything, because the clients send private keys and credentials and any local
// user can create \\.\pipe\plaitway while the service is not running. DialOptions
// reads the owner of the pipe instance it has connected to and accepts SYSTEM
// and Administrators (the service, or an elevated development daemon) and the
// calling user (an unelevated development daemon); every other owner, and any
// failure of the check, closes the connection with an error that starts with
// RefusedServerPrefix.
//
// The owner is used because it is the one property of the server that an
// unprivileged client can read and an unprivileged server cannot forge: asking
// for another owner when creating a pipe fails with ERROR_INVALID_OWNER, so a
// pipe made by a standard user is always owned by that user. The alternative of
// opening the server process (GetNamedPipeServerProcessId, OpenProcessToken)
// does not work for the production case, since a standard user is denied access
// to a LocalSystem process.
//
// Accepting the calling user's own pipe has a residual: while the service is
// down, another process of the same user can serve \\.\pipe\plaitway and receive
// what the CLI sends. It is accepted because an unelevated development daemon is
// owned by that user and the process could already read everything else the user
// owns. A separate name for development daemons would remove it.
package transport
