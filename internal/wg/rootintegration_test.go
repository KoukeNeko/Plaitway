//go:build rootintegration && darwin

package wg

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/tun"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// These tests create real utun devices, configure them with /sbin/ifconfig and
// add one route, so they need root. They refuse to run otherwise:
//
//	go test -c -tags rootintegration -o wg.test ./internal/wg
//	sudo PLAITWAY_ROOT_TESTS=1 ./wg.test -test.run TestRoot -test.v
//
// Everything lives in the documentation ranges 198.51.100.0/24 and
// 203.0.113.0/24, and the route is bound to a utun that dies with the test.
func requireRoot(t *testing.T) {
	t.Helper()
	if os.Getenv("PLAITWAY_ROOT_TESTS") != "1" || os.Geteuid() != 0 {
		t.Skip("needs PLAITWAY_ROOT_TESTS=1 and effective uid 0")
	}
}

// spyTun is a real utun that also reports the packets wireguard-go writes into
// it, as the far end of a tunnel on a host that has no second network stack.
type spyTun struct {
	tun.Device
	written chan []byte
}

func (s *spyTun) Write(bufs [][]byte, offset int) (int, error) {
	n, err := s.Device.Write(bufs, offset)
	for _, buf := range bufs[:n] {
		select {
		case s.written <- append([]byte(nil), buf[offset:]...):
		default:
		}
	}
	return n, err
}

// isEchoRequestTo reports whether packet is an IPv4 ICMP echo request to dst.
func isEchoRequestTo(packet []byte, dst string) bool {
	const ipv4HeaderLen = 20
	return len(packet) > ipv4HeaderLen && packet[0]>>4 == 4 && packet[9] == 1 &&
		net.IP(packet[16:20]).String() == dst && packet[ipv4HeaderLen] == 8
}

func waitForEchoRequest(packets <-chan []byte, dst string, limit time.Duration) bool {
	deadline := time.After(limit)
	for {
		select {
		case packet := <-packets:
			if isEchoRequestTo(packet, dst) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

func newRootEngine(t *testing.T, name, content string, cfg Config) (*engine, *statusLog) {
	t.Helper()
	cfg.pollInterval, cfg.stallGrace = testPoll, testGrace
	deps := tunnel.Deps{Network: newRecorder(), Net: fakeMonitor{}, Log: (&logSink{}).log}
	eng, err := newEngine(cfg.withDefaults(), tunnel.Spec{Owner: tunnel.OwnerID(name), Name: name, Content: []byte(content)}, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := eng.Stop(context.Background()); err != nil {
			t.Errorf("Stop(%s): %v", name, err)
		}
	})
	return eng, collectStatus(eng.Status())
}

func TestRootTwoUtunTunnelsCarryAPing(t *testing.T) {
	requireRoot(t)
	checkLeaks(t)

	const (
		serverAddr = "198.51.100.1"
		clientAddr = "198.51.100.2"
		farNet     = "203.0.113.0/24"
		farHost    = "203.0.113.7"
	)
	serverKey, clientKey := newKeyPair(t), newKeyPair(t)
	port := freeUDPPort(t)
	serverContent := fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = %s/32\nListenPort = %d\n\n[Peer]\nPublicKey = %s\nAllowedIPs = %s/32\n",
		serverKey.private, serverAddr, port, clientKey.public, clientAddr)
	clientContent := fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = %s/32\n\n[Peer]\nPublicKey = %s\nEndpoint = 127.0.0.1:%d\nAllowedIPs = %s\n",
		clientKey.private, clientAddr, serverKey.public, port, farNet)

	spy := &spyTun{written: make(chan []byte, 64)}
	server, serverStatus := newRootEngine(t, "root-server", serverContent, Config{
		TunFactory: func(mtu int) (tun.Device, error) {
			dev, err := tun.CreateTUN("utun", mtu)
			if err != nil {
				return nil, err
			}
			spy.Device = dev
			return spy, nil
		},
	})
	client, clientStatus := newRootEngine(t, "root-client", clientContent, Config{})
	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Start returns at once. A client that shakes hands before the server's
	// socket is bound waits RekeyTimeout for its next try, so the client starts
	// when the server's interface has its address, which its own setup outlasts.
	eventually(t, "the server's interface to have its address", func() bool {
		netIface, err := net.InterfaceByName(serverStatus.last().Iface)
		if err != nil {
			return false
		}
		addrs, _ := netIface.Addrs()
		return containsIP(addrs, serverAddr)
	})
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	eventually(t, "both tunnels to come up", func() bool {
		return serverStatus.last().State == tunnel.StateUp && clientStatus.last().State == tunnel.StateUp
	})

	serverIface, clientIface := serverStatus.last().Iface, clientStatus.last().Iface
	for iface, want := range map[string]string{serverIface: serverAddr, clientIface: clientAddr} {
		netIface, err := net.InterfaceByName(iface)
		if err != nil {
			t.Fatalf("interface %s: %v", iface, err)
		}
		addrs, _ := netIface.Addrs()
		if !containsIP(addrs, want) {
			t.Errorf("%s has addresses %v, want %s", iface, addrs, want)
		}
	}

	// The Reconciler is not part of this test, so the route is added by hand.
	if out, err := exec.Command("/sbin/route", "-n", "add", "-net", farNet, "-interface", clientIface).CombinedOutput(); err != nil {
		t.Fatalf("route add: %v: %s", err, out)
	}
	t.Cleanup(func() { exec.Command("/sbin/route", "-n", "delete", "-net", farNet, "-interface", clientIface).Run() })

	// Nothing answers on the far side, so the exit status of ping means nothing;
	// what counts is that the request came out of the server's utun.
	pingOutput, _ := exec.Command("/sbin/ping", "-c", "3", "-t", "3", "-S", clientAddr, farHost).CombinedOutput()
	if !waitForEchoRequest(spy.written, farHost, waitTimeout) {
		t.Fatalf("no echo request to %s came out of %s; ping said:\n%s", farHost, serverIface, pingOutput)
	}
	eventually(t, "the client's counters", func() bool { return clientStatus.last().Stats.TxBytes > 0 })

	for _, eng := range []*engine{client, server} {
		if err := eng.Stop(context.Background()); err != nil {
			t.Errorf("Stop: %v", err)
		}
	}
	for _, iface := range []string{serverIface, clientIface} {
		eventually(t, iface+" to disappear", func() bool {
			_, err := net.InterfaceByName(iface)
			return err != nil
		})
	}
}

func containsIP(addrs []net.Addr, want string) bool {
	for _, a := range addrs {
		if ipNet, ok := a.(*net.IPNet); ok && ipNet.IP.String() == want {
			return true
		}
	}
	return false
}
