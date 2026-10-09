package main

import (
	"fmt"
	"net"
)

// The states of the systemd notification protocol (sd_notify(3)) the daemon
// reports.
const (
	notifyReady    = "READY=1"
	notifyStopping = "STOPPING=1"
)

// notify tells systemd that the daemon reached state. Failing to do so is
// logged and nothing more: the daemon works without anybody listening.
func (d *daemon) notify(state string) {
	if err := sendNotification(d.notifySocket, state); err != nil {
		d.log.Warn("cannot notify systemd", "state", state, "err", err)
	}
}

// sendNotification sends state as one datagram to the socket systemd names in
// $NOTIFY_SOCKET: a path, or an abstract socket when it starts with "@" (Go
// takes that spelling for an abstract address). It does nothing without one,
// which is every run that systemd did not start with Type=notify.
func sendNotification(socket, state string) error {
	if socket == "" {
		return nil
	}
	if socket[0] != '/' && socket[0] != '@' {
		return fmt.Errorf("notification socket %q is neither a path nor an abstract socket", socket)
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		return err
	}
	if _, err := conn.Write([]byte(state)); err != nil {
		conn.Close()
		return err
	}
	return conn.Close()
}
