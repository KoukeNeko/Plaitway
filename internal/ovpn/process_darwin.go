package ovpn

import "os/exec"

// childEnv is the whole environment of the openvpn child. It runs
// /sbin/ifconfig by absolute path; nothing from the daemon's environment is
// needed, and none is passed on.
func childEnv() []string { return []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"} }

// prepareCommand sets the OS specific attributes of the child before it starts.
func prepareCommand(*exec.Cmd, string) {}
