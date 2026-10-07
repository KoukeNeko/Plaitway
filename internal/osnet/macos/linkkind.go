package macos

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

const (
	networksetupPath = "/usr/sbin/networksetup"
	// linkRetry is how long after a failed read of the hardware ports the next
	// read waits. Snapshot is on the Reconciler's path, and a networksetup that
	// hangs would hold it for the whole timeout on every call.
	linkRetry = time.Minute
)

// linkKinds tells what kind of network an interface connects to, from the
// hardware port names of networksetup. The table is read when an interface
// shows up that it does not list, not for every question: adapters come and go.
type linkKinds struct {
	run CommandRunner
	log *slog.Logger
	now func() time.Time

	mu sync.Mutex
	// ports maps a device name to its hardware port name, as of the last read.
	ports   map[string]string
	retryAt time.Time
}

func newLinkKinds(run CommandRunner, log *slog.Logger) *linkKinds {
	return &linkKinds{run: run, log: log, now: time.Now}
}

// kind is LinkOther for a device that is not a hardware port (a tunnel, a
// bridge) and for one whose port cannot be looked up.
func (k *linkKinds) kind(device string) osnet.LinkKind {
	k.mu.Lock()
	defer k.mu.Unlock()
	port, ok := k.ports[device]
	if !ok && !k.now().Before(k.retryAt) {
		k.load()
		port, ok = k.ports[device]
	}
	if !ok {
		return osnet.LinkOther
	}
	return classifyPort(port)
}

// load replaces the table. The kinds only serve rules about the network the Mac
// is on and the Reconciler does not need them, so a failed read is logged, not
// returned, and the old table stays.
func (k *linkKinds) load() {
	ports, err := k.readPorts()
	if err != nil {
		k.retryAt = k.now().Add(linkRetry)
		k.log.Warn("read hardware ports; link kinds may be wrong", "err", err, "retry_in", linkRetry)
		return
	}
	k.ports = ports
}

func (k *linkKinds) readPorts() (map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	out, err := k.run(ctx, networksetupPath, []string{"-listallhardwareports"}, "")
	if err != nil {
		return nil, fmt.Errorf("networksetup -listallhardwareports: %w: %s", err, strings.TrimSpace(string(out)))
	}
	ports := parseHardwarePorts(string(out))
	if len(ports) == 0 {
		return nil, errors.New("networksetup -listallhardwareports lists no hardware port")
	}
	return ports, nil
}

// parseHardwarePorts reads the output of networksetup -listallhardwareports:
// blocks of "Hardware Port: <name>" and "Device: <interface>" lines, then a
// section of VLANs that holds no ports.
func parseHardwarePorts(out string) map[string]string {
	ports := make(map[string]string)
	var port string
	for line := range strings.Lines(out) {
		line = strings.TrimSpace(line)
		if name, ok := strings.CutPrefix(line, "Hardware Port: "); ok {
			port = name
		} else if device, ok := strings.CutPrefix(line, "Device: "); ok && port != "" {
			ports[device] = port
			port = ""
		}
	}
	return ports
}

// classifyPort sorts a hardware port by its name. Apple names the ports that
// are not a network of their own: phones and tablets over USB, Bluetooth, the
// Thunderbolt Bridge and the Thunderbolt links behind it. Every other port is a
// wired adapter, which is how a USB adapter named after its chip (AX88179A)
// shows up.
func classifyPort(port string) osnet.LinkKind {
	name := strings.ToLower(port)
	switch {
	case containsAny(name, "wi-fi", "airport"):
		return osnet.LinkWiFi
	case strings.Contains(name, "ethernet"): // before Thunderbolt: Thunderbolt Ethernet is wired
		return osnet.LinkEthernet
	case containsAny(name, "iphone", "ipad", "bluetooth", "bridge", "thunderbolt", "firewire"):
		return osnet.LinkOther
	}
	return osnet.LinkEthernet
}

func containsAny(s string, substrings ...string) bool {
	for _, sub := range substrings {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
