package main

import "os"

// newPolicy: a development daemon, which is not root, has the user who started
// it as its administrator.
func newPolicy() *policy {
	var daemonUID *uint32
	if !isPrivileged() {
		euid := uint32(os.Geteuid())
		daemonUID = &euid
	}
	return linuxPolicy(systemAccounts(), logindSeatsDir, daemonUID)
}
