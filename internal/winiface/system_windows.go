package winiface

import (
	"errors"
	"fmt"
	"net/netip"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/KoukeNeko/Plaitway/internal/winiface/inet"
)

// Both origins of an address configured by hand or through the API are
// NlpoManual and NlsoManual.
const originManual = 1

var (
	iphlpapi = windows.NewLazySystemDLL("iphlpapi.dll")

	procConvertInterfaceAliasToLuid     = iphlpapi.NewProc("ConvertInterfaceAliasToLuid")
	procConvertInterfaceLuidToIndex     = iphlpapi.NewProc("ConvertInterfaceLuidToIndex")
	procConvertInterfaceLuidToAlias     = iphlpapi.NewProc("ConvertInterfaceLuidToAlias")
	procInitializeUnicastIpAddressEntry = iphlpapi.NewProc("InitializeUnicastIpAddressEntry")
	procCreateUnicastIpAddressEntry     = iphlpapi.NewProc("CreateUnicastIpAddressEntry")
	procDeleteUnicastIpAddressEntry     = iphlpapi.NewProc("DeleteUnicastIpAddressEntry")
	procSetIpInterfaceEntry             = iphlpapi.NewProc("SetIpInterfaceEntry")
)

// host is the real IP Helper API.
var host system = windowsSystem{}

type windowsSystem struct{}

// netioCall calls an IP Helper function that returns a NETIO_STATUS: zero for
// success, a Win32 error code otherwise.
func netioCall(proc *windows.LazyProc, args ...uintptr) error {
	status, _, _ := proc.Call(args...)
	if status != 0 {
		return syscall.Errno(status)
	}
	return nil
}

// notFound maps the errors the conversion functions use for an unknown
// interface to ErrNotFound. ConvertInterfaceAliasToLuid reports an alias it
// does not know as ERROR_INVALID_PARAMETER.
func notFound(err error) error {
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_NOT_FOUND) {
		return ErrNotFound
	}
	return err
}

func (windowsSystem) luidFromName(name string) (uint64, error) {
	alias, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	var luid uint64
	err = netioCall(procConvertInterfaceAliasToLuid, uintptr(unsafe.Pointer(alias)), uintptr(unsafe.Pointer(&luid)))
	if err != nil {
		return 0, notFound(err)
	}
	return luid, nil
}

func (windowsSystem) indexFromLUID(luid uint64) (uint32, error) {
	var index uint32
	if err := netioCall(procConvertInterfaceLuidToIndex, uintptr(unsafe.Pointer(&luid)), uintptr(unsafe.Pointer(&index))); err != nil {
		return 0, notFound(err)
	}
	return index, nil
}

func (windowsSystem) nameFromLUID(luid uint64) (string, error) {
	var alias [windows.IF_MAX_STRING_SIZE + 1]uint16
	err := netioCall(procConvertInterfaceLuidToAlias, uintptr(unsafe.Pointer(&luid)), uintptr(unsafe.Pointer(&alias[0])), uintptr(len(alias)))
	if err != nil {
		return "", notFound(err)
	}
	return windows.UTF16ToString(alias[:]), nil
}

func (windowsSystem) addresses(luid uint64) ([]address, error) {
	var table *windows.MibUnicastIpAddressTable
	if err := windows.GetUnicastIpAddressTable(windows.AF_UNSPEC, &table); err != nil {
		return nil, fmt.Errorf("GetUnicastIpAddressTable: %w", err)
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))
	var out []address
	for _, row := range unsafe.Slice(&table.Table[0], table.NumEntries) {
		if row.InterfaceLuid == luid {
			out = append(out, addressOfRow(&row))
		}
	}
	return out, nil
}

func addressOfRow(row *windows.MibUnicastIpAddressRow) address {
	addr := inet.Addr((*windows.RawSockaddrInet)(unsafe.Pointer(&row.Address)))
	return address{
		Prefix: netip.PrefixFrom(addr, int(row.OnLinkPrefixLength)),
		Manual: row.PrefixOrigin == originManual && row.SuffixOrigin == originManual,
		DAD:    dadState(row.DadState),
	}
}

func addressRow(luid uint64, addr netip.Addr) windows.MibUnicastIpAddressRow {
	var row windows.MibUnicastIpAddressRow
	procInitializeUnicastIpAddressEntry.Call(uintptr(unsafe.Pointer(&row)))
	row.InterfaceLuid = luid
	inet.Set((*windows.RawSockaddrInet)(unsafe.Pointer(&row.Address)), addr)
	return row
}

func (windowsSystem) addAddress(luid uint64, p netip.Prefix) error {
	row := addressRow(luid, p.Addr())
	row.OnLinkPrefixLength = uint8(p.Bits())
	return netioCall(procCreateUnicastIpAddressEntry, uintptr(unsafe.Pointer(&row)))
}

func (s windowsSystem) deleteAddress(luid uint64, a netip.Addr) error {
	row := addressRow(luid, a)
	err := netioCall(procDeleteUnicastIpAddressEntry, uintptr(unsafe.Pointer(&row)))
	return forgiveAbsentAddress(err, func() (bool, error) {
		state, stateErr := s.addressState(luid, a)
		return state != dadInvalid, stateErr
	})
}

// forgiveAbsentAddress turns the failure of a delete into success when the
// address is not there, which is what the delete wanted. Windows answers "not
// found" for an address that is gone from an adapter, and "the parameter is
// incorrect" for one that is gone from the loopback (found by
// TestRootSetAddressesOnLoopback); a parameter that really is wrong must still
// fail, so that answer is believed only when the address is looked up and is not
// there.
func forgiveAbsentAddress(err error, stillThere func() (bool, error)) error {
	switch {
	case err == nil, errors.Is(err, windows.ERROR_NOT_FOUND):
		return nil // gone already: the goal is reached
	case errors.Is(err, windows.ERROR_INVALID_PARAMETER):
		if there, lookupErr := stillThere(); lookupErr == nil && !there {
			return nil
		}
	}
	return err
}

func (windowsSystem) addressState(luid uint64, a netip.Addr) (dadState, error) {
	row := addressRow(luid, a)
	err := windows.GetUnicastIpAddressEntry(&row)
	switch {
	case errors.Is(err, windows.ERROR_NOT_FOUND):
		return dadInvalid, nil
	case err != nil:
		return dadInvalid, fmt.Errorf("GetUnicastIpAddressEntry: %w", err)
	}
	return dadState(row.DadState), nil
}

func (windowsSystem) changeIPInterface(luid uint64, family Family, change func(*ipInterface)) error {
	row := windows.MibIpInterfaceRow{Family: uint16(family), InterfaceLuid: luid}
	if err := windows.GetIpInterfaceEntry(&row); err != nil {
		if errors.Is(err, windows.ERROR_NOT_FOUND) {
			return errFamilyNotBound
		}
		return fmt.Errorf("GetIpInterfaceEntry: %w", err)
	}
	edit := ipInterface{MTU: row.NlMtu, Metric: row.Metric, AutoMetric: row.UseAutomaticMetric != 0}
	change(&edit)
	row.NlMtu, row.Metric, row.UseAutomaticMetric = edit.MTU, edit.Metric, boolByte(edit.AutoMetric)
	if family == IPv4 {
		// SetIpInterfaceEntry rejects a nonzero site prefix length on IPv4.
		row.SitePrefixLength = 0
	}
	if err := netioCall(procSetIpInterfaceEntry, uintptr(unsafe.Pointer(&row))); err != nil {
		return fmt.Errorf("SetIpInterfaceEntry: %w", err)
	}
	return nil
}

func boolByte(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

func (windowsSystem) isUp(luid uint64) (bool, error) {
	row := windows.MibIfRow2{InterfaceLuid: luid}
	if err := windows.GetIfEntry2Ex(windows.MibIfEntryNormalWithoutStatistics, &row); err != nil {
		if errors.Is(err, windows.ERROR_NOT_FOUND) {
			return false, ErrNotFound
		}
		return false, fmt.Errorf("GetIfEntry2Ex: %w", err)
	}
	return row.OperStatus == windows.IfOperStatusUp, nil
}
