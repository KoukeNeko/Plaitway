package main

import (
	"fmt"
	"testing"
	"time"
)

// systemd names a socket in the abstract namespace with a leading "@".
func TestSendNotificationReachesAnAbstractSocket(t *testing.T) {
	name := fmt.Sprintf("@plaitway-test-%d", time.Now().UnixNano())
	systemd := listenNotify(t, name)
	if err := sendNotification(name, notifyReady); err != nil {
		t.Fatal(err)
	}
	if got := systemd.next(t); got != "READY=1" {
		t.Fatalf("systemd received %q, want READY=1", got)
	}
}
