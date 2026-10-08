//go:build !windows

package main

// wantDefaultSocket is the path the LaunchDaemon's plist and the app name.
const wantDefaultSocket = "/var/run/plaitway/plaitwayd.sock"
