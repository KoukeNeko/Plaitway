package main

import "golang.org/x/sys/windows"

// isPrivileged reports whether the process token is elevated, which a service
// running as LocalSystem is. os.Geteuid is -1 on Windows, so it cannot say.
func isPrivileged() bool { return windows.GetCurrentProcessToken().IsElevated() }
