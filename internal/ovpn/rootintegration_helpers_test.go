//go:build rootintegration

package ovpn

import (
	"net"
	"testing"
)

func requireInterfaceAddress(t *testing.T, name, want string) {
	t.Helper()
	iface, err := net.InterfaceByName(name)
	if err != nil {
		t.Fatalf("interface %s: %v", name, err)
	}
	addrs, err := iface.Addrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.String() == want {
			return
		}
	}
	t.Fatalf("%s has addresses %v, want %s", name, addrs, want)
}
