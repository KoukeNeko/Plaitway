//go:build rootintegration && unix && !linux

package ovpn

import "testing"

// requirePrivateNetns is for Linux, where the root tests would otherwise change
// the network of the host; see rootlab_linux_test.go.
func requirePrivateNetns(*testing.T) {}
