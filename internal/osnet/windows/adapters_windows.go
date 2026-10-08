package windows

import (
	"errors"
	"fmt"
	"net/netip"
	"unsafe"

	win "golang.org/x/sys/windows"

	"github.com/KoukeNeko/Plaitway/internal/winiface/inet"
)

// Bits of InterfaceAndOperStatusFlags.
const (
	interfaceFlagHardware = 1 << 0 // HardwareInterface
	// interfaceFlagFilter marks the NDIS filter drivers (QoS Packet Scheduler,
	// WFP, Hyper-V switch extensions) that the table lists as an interface of
	// their own next to the adapter they filter. They carry no traffic of their own.
	interfaceFlagFilter = 1 << 1 // FilterInterface
)

// readAdapters lists every interface with its type, state and names. Addresses
// and metrics are left empty.
func readAdapters() ([]adapter, error) {
	var table *win.MibIfTable2
	if err := win.GetIfTable2Ex(win.MibIfEntryNormalWithoutStatistics, &table); err != nil {
		return nil, fmt.Errorf("GetIfTable2Ex: %w", err)
	}
	defer win.FreeMibTable(unsafe.Pointer(table))
	return adaptersOfRows(unsafe.Slice(&table.Table[0], table.NumEntries)), nil
}

// adaptersOfRows converts the rows of the interface table, without the filter
// pseudo-interfaces.
func adaptersOfRows(rows []win.MibIfRow2) []adapter {
	adapters := make([]adapter, 0, len(rows))
	for i := range rows {
		if rows[i].InterfaceAndOperStatusFlags&interfaceFlagFilter == 0 {
			adapters = append(adapters, adapterOfRow(&rows[i]))
		}
	}
	return adapters
}

func adapterOfRow(row *win.MibIfRow2) adapter {
	return adapter{
		LUID:        row.InterfaceLuid,
		Index:       row.InterfaceIndex,
		Name:        win.UTF16ToString(row.Alias[:]),
		Description: win.UTF16ToString(row.Description[:]),
		Type:        row.Type,
		Hardware:    row.InterfaceAndOperStatusFlags&interfaceFlagHardware != 0,
		Up:          row.OperStatus == win.IfOperStatusUp,
	}
}

// listAdapters is readAdapters plus the unicast addresses and the interface
// metrics, which is what a NetState is made of.
func listAdapters() ([]adapter, error) {
	adapters, err := readAdapters()
	if err != nil {
		return nil, err
	}
	addrs, err := readAddresses()
	if err != nil {
		return nil, err
	}
	for i := range adapters {
		adapters[i].Addrs = addrs[adapters[i].LUID]
		if !adapters[i].Up {
			continue
		}
		if adapters[i].Metric4, err = readInterfaceMetric(adapters[i].LUID, win.AF_INET); err != nil {
			return nil, err
		}
		if adapters[i].Metric6, err = readInterfaceMetric(adapters[i].LUID, win.AF_INET6); err != nil {
			return nil, err
		}
	}
	return adapters, nil
}

// readAddresses returns the unicast addresses by interface LUID. An address
// that failed duplicate address detection is not usable and is left out.
func readAddresses() (map[uint64][]netip.Prefix, error) {
	var table *win.MibUnicastIpAddressTable
	if err := win.GetUnicastIpAddressTable(win.AF_UNSPEC, &table); err != nil {
		return nil, fmt.Errorf("GetUnicastIpAddressTable: %w", err)
	}
	defer win.FreeMibTable(unsafe.Pointer(table))
	return addressesOfRows(unsafe.Slice(&table.Table[0], table.NumEntries)), nil
}

// addressesOfRows groups the rows of the unicast address table by interface.
func addressesOfRows(rows []win.MibUnicastIpAddressRow) map[uint64][]netip.Prefix {
	byLUID := make(map[uint64][]netip.Prefix)
	for i := range rows {
		row := &rows[i]
		if row.DadState == win.IpDadStateDuplicate || row.DadState == win.IpDadStateInvalid {
			continue
		}
		addr := inet.Addr((*win.RawSockaddrInet)(unsafe.Pointer(&row.Address)))
		if prefix := netip.PrefixFrom(addr, int(row.OnLinkPrefixLength)); prefix.IsValid() {
			byLUID[row.InterfaceLuid] = append(byLUID[row.InterfaceLuid], prefix)
		}
	}
	return byLUID
}

// readInterfaceMetric returns the interface metric of one family. An adapter
// without that IP stack (IPv6 is unbound from many virtual adapters) has none:
// zero, which cannot win over a real metric by accident because such an adapter
// has no route of that family either.
func readInterfaceMetric(luid uint64, family uint16) (uint32, error) {
	row := win.MibIpInterfaceRow{Family: family, InterfaceLuid: luid}
	err := win.GetIpInterfaceEntry(&row)
	switch {
	case errors.Is(err, win.ERROR_NOT_FOUND):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("GetIpInterfaceEntry for interface %#x, family %d: %w", luid, family, err)
	}
	return row.Metric, nil
}
