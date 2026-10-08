//go:build windows

// loopbackwatch logs, once per distinct endpoint, every UDP or listening TCP socket whose local address is not
// loopback and whose owner is a process named like a Go test binary. It polls the kernel tables without
// sleeping, so a socket that lives for a millisecond is still seen. Read-only.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	afInet                   = 2
	afInet6                  = 23
	udpTableOwnerPid         = 1
	tcpTableOwnerPidListener = 3
	errorInsufficientBuffer  = 122
	processQueryLimitedInfo  = 0x1000
)

var (
	iphlpapi                = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedUdpTable = iphlpapi.NewProc("GetExtendedUdpTable")
	procGetExtendedTcpTable = iphlpapi.NewProc("GetExtendedTcpTable")
)

type endpoint struct {
	proto string
	addr  string
	port  uint16
	pid   uint32
}

func read(proc *windows.LazyProc, family, class uintptr) []byte {
	size := uint32(1 << 14)
	for {
		buffer := make([]byte, size)
		result, _, _ := proc.Call(uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)), 0, family, class, 0)
		switch result {
		case 0:
			return buffer
		case errorInsufficientBuffer:
			continue
		default:
			return nil
		}
	}
}

func port(raw uint32) uint16 { return uint16(raw>>8&0xff | raw<<8&0xff00) }

func v4(raw uint32) string {
	return fmt.Sprintf("%d.%d.%d.%d", byte(raw), byte(raw>>8), byte(raw>>16), byte(raw>>24))
}

func v6(raw [16]byte) string {
	var b strings.Builder
	for i := 0; i < 16; i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		fmt.Fprintf(&b, "%x", uint16(raw[i])<<8|uint16(raw[i+1]))
	}
	return b.String()
}

func isLoopback(addr string) bool {
	return strings.HasPrefix(addr, "127.") || addr == "0:0:0:0:0:0:0:1"
}

func snapshot() []endpoint {
	var found []endpoint
	if buffer := read(procGetExtendedUdpTable, afInet, udpTableOwnerPid); buffer != nil {
		count := *(*uint32)(unsafe.Pointer(&buffer[0]))
		for i := uint32(0); i < count; i++ {
			row := (*[3]uint32)(unsafe.Pointer(&buffer[4+i*12]))
			found = append(found, endpoint{"udp4", v4(row[0]), port(row[1]), row[2]})
		}
	}
	if buffer := read(procGetExtendedUdpTable, afInet6, udpTableOwnerPid); buffer != nil {
		count := *(*uint32)(unsafe.Pointer(&buffer[0]))
		const rowSize = 16 + 4 + 4 + 4
		for i := uint32(0); i < count; i++ {
			base := 4 + i*rowSize
			var raw [16]byte
			copy(raw[:], buffer[base:base+16])
			found = append(found, endpoint{"udp6", v6(raw), port(*(*uint32)(unsafe.Pointer(&buffer[base+20]))), *(*uint32)(unsafe.Pointer(&buffer[base+24]))})
		}
	}
	if buffer := read(procGetExtendedTcpTable, afInet, tcpTableOwnerPidListener); buffer != nil {
		count := *(*uint32)(unsafe.Pointer(&buffer[0]))
		for i := uint32(0); i < count; i++ {
			row := (*[6]uint32)(unsafe.Pointer(&buffer[4+i*24]))
			found = append(found, endpoint{"tcp4", v4(row[1]), port(row[2]), row[5]})
		}
	}
	return found
}

func imageName(pid uint32, cache map[uint32]string) string {
	if name, ok := cache[pid]; ok {
		return name
	}
	name := ""
	if handle, err := windows.OpenProcess(processQueryLimitedInfo, false, pid); err == nil {
		buffer := make([]uint16, 1024)
		size := uint32(len(buffer))
		if windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size) == nil {
			name = windows.UTF16ToString(buffer[:size])
		}
		windows.CloseHandle(handle)
	}
	cache[pid] = name
	return name
}

func main() {
	log := os.Stdout
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	seen := map[endpoint]bool{}
	names := map[uint32]string{}
	for {
		select {
		case <-stop:
			return
		default:
		}
		for _, e := range snapshot() {
			if isLoopback(e.addr) || seen[e] {
				continue
			}
			name := imageName(e.pid, names)
			if !strings.HasSuffix(strings.ToLower(name), ".test.exe") {
				continue
			}
			seen[e] = true
			fmt.Fprintf(log, "%s %s %s:%d pid=%d %s\n", time.Now().Format("15:04:05.000"), e.proto, e.addr, e.port, e.pid, name)
		}
	}
}
