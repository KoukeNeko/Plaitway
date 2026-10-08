package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// socketModeAttrs is empty: the pipe's access list is its access control, and a
// mode in the log line would read as if it were.
func socketModeAttrs(os.FileMode) []any { return nil }

// processIdentityAttrs are the log attributes that say who the daemon runs as.
// There is no uid on Windows: os.Getuid is -1.
func processIdentityAttrs() []any {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return []any{"user_err", err, "privileged", isPrivileged()}
	}
	return []any{"user", user.User.Sid.String(), "privileged", isPrivileged()}
}
