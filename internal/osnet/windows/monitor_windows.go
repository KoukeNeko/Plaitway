package windows

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	win "golang.org/x/sys/windows"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/winiface/inet"
)

// Power notification types (PBT_*) that say the machine is running again.
const (
	pbtAPMResumeCritical  = 0x6
	pbtAPMResumeSuspend   = 0x7
	pbtAPMResumeAutomatic = 0x12
	// deviceNotifyCallback asks for a callback instead of window messages, which
	// is what makes the registration work in a service and in a console program.
	deviceNotifyCallback = 2
)

var (
	powrprof = win.NewLazySystemDLL("powrprof.dll")

	procPowerRegisterSuspendResumeNotification   = powrprof.NewProc("PowerRegisterSuspendResumeNotification")
	procPowerUnregisterSuspendResumeNotification = powrprof.NewProc("PowerUnregisterSuspendResumeNotification")
)

// NewNetMonitor returns the network monitor of this PC.
func NewNetMonitor(opts NetMonitorOptions) osnet.NetMonitor {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &netMonitor{
		log:          log,
		clk:          newRealClock(),
		routes:       NewRouteTable(),
		adapters:     listAdapters,
		watchChanges: func(ctx context.Context, changed func()) error { return watchChanges(ctx, log, changed) },
		watchWake:    func(ctx context.Context, woke func()) error { return watchWake(ctx, log, woke) },
	}
}

// registration is what the system's callbacks find by the context pointer they
// were registered with. The pointer is a real one, to a value kept alive by
// registrations until the notification is cancelled.
type registration struct{ signal func() }

var (
	registrations sync.Map // uintptr(*registration) -> *registration
	// activeNotifications counts the notifications that are registered with the
	// system, so that tests can see that none is left behind.
	activeNotifications atomic.Int32
)

func addRegistration(signal func()) (reg *registration, key uintptr) {
	reg = &registration{signal: signal}
	key = uintptr(unsafe.Pointer(reg))
	registrations.Store(key, reg)
	return reg, key
}

func lookupRegistration(key uintptr) (*registration, bool) {
	v, ok := registrations.Load(key)
	if !ok {
		return nil, false
	}
	return v.(*registration), true
}

// The callbacks are created once: the runtime never frees one that
// NewCallback made. They run on a thread of the system's pool and must not
// block.
var (
	routeCallback = win.NewCallback(func(callerContext, row, notificationType uintptr) uintptr {
		onRouteNotification(callerContext, (*win.MibIpForwardRow2)(pointerOf(row)))
		return 0
	})
	interfaceCallback = win.NewCallback(func(callerContext, row, notificationType uintptr) uintptr {
		signalRegistration(callerContext)
		return 0
	})
	addressCallback = win.NewCallback(func(callerContext, row, notificationType uintptr) uintptr {
		onAddressNotification(callerContext, (*win.MibUnicastIpAddressRow)(pointerOf(row)))
		return 0
	})
	resumeCallback = win.NewCallback(func(callerContext, powerEvent, setting uintptr) uintptr {
		switch powerEvent {
		case pbtAPMResumeAutomatic, pbtAPMResumeSuspend, pbtAPMResumeCritical:
			signalRegistration(callerContext)
		}
		return 0
	})
)

// pointerOf turns the address the system passed to a callback back into a
// pointer. The memory belongs to the system and is valid until the callback
// returns.
func pointerOf(address uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&address)) }

func signalRegistration(key uintptr) {
	if reg, ok := lookupRegistration(key); ok {
		reg.signal()
	}
}

// onRouteNotification signals unless the row is housekeeping of the system. A
// notification without a row cannot be judged and counts.
func onRouteNotification(key uintptr, row *win.MibIpForwardRow2) {
	if row != nil {
		dst := netip.PrefixFrom(inet.Addr(&row.DestinationPrefix.Prefix), int(row.DestinationPrefix.PrefixLength))
		if !routeNotificationRelevant(dst) {
			return
		}
	}
	signalRegistration(key)
}

func onAddressNotification(key uintptr, row *win.MibUnicastIpAddressRow) {
	if row != nil && !addressNotificationRelevant(inet.Addr((*win.RawSockaddrInet)(unsafe.Pointer(&row.Address)))) {
		return
	}
	signalRegistration(key)
}

// notifier registers one kind of MIB change notification.
type notifier func(family uint16, callback uintptr, callerContext unsafe.Pointer, initialNotification bool, handle *win.Handle) error

// watchChanges registers for changes of routes, IP interfaces and unicast
// addresses of both families, and holds the registrations until ctx ends.
func watchChanges(ctx context.Context, log *slog.Logger, changed func()) error {
	reg, key := addRegistration(changed)
	defer registrations.Delete(key)
	var handles []win.Handle
	defer func() {
		// Cancelling waits for a callback that is running, so none runs after it.
		for _, handle := range handles {
			if err := win.CancelMibChangeNotify2(handle); err != nil {
				log.Warn("cancel a change notification", "err", err)
			}
			activeNotifications.Add(-1)
		}
	}()
	for _, n := range []struct {
		name     string
		register notifier
		callback uintptr
	}{
		{"routes", win.NotifyRouteChange2, routeCallback},
		{"IP interfaces", win.NotifyIpInterfaceChange, interfaceCallback},
		{"unicast addresses", win.NotifyUnicastIpAddressChange, addressCallback},
	} {
		var handle win.Handle
		if err := n.register(win.AF_UNSPEC, n.callback, unsafe.Pointer(reg), false, &handle); err != nil {
			return fmt.Errorf("register for changes of %s: %w", n.name, err)
		}
		activeNotifications.Add(1)
		handles = append(handles, handle)
	}
	<-ctx.Done()
	return nil
}

// deviceNotifySubscribeParameters is DEVICE_NOTIFY_SUBSCRIBE_PARAMETERS.
type deviceNotifySubscribeParameters struct {
	Callback uintptr
	Context  uintptr
}

// watchWake registers for the machine resuming from sleep. The registration is
// a callback, not a window message: a service has no window, and a console
// program would have to create one.
func watchWake(ctx context.Context, log *slog.Logger, woke func()) error {
	_, key := addRegistration(woke)
	defer registrations.Delete(key)
	params := &deviceNotifySubscribeParameters{Callback: resumeCallback, Context: key}
	var handle uintptr
	status, _, _ := procPowerRegisterSuspendResumeNotification.Call(
		deviceNotifyCallback, uintptr(unsafe.Pointer(params)), uintptr(unsafe.Pointer(&handle)))
	if status != 0 {
		return fmt.Errorf("PowerRegisterSuspendResumeNotification: %w", syscall.Errno(status))
	}
	activeNotifications.Add(1)
	defer activeNotifications.Add(-1)
	defer runtime.KeepAlive(params)
	defer func() {
		if status, _, _ := procPowerUnregisterSuspendResumeNotification.Call(handle); status != 0 {
			log.Warn("unregister the sleep notification", "err", syscall.Errno(status))
		}
	}()
	<-ctx.Done()
	return nil
}
