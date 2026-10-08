package wg

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	// wintunHardwareID is the hardware ID of every wintun adapter, whoever
	// created it.
	wintunHardwareID = "Wintun"

	// networkConnectionsKey holds, below a network class GUID, one key per
	// adapter named after its NetCfgInstanceId, with the name the user sees in
	// the subkey "Connection".
	networkConnectionsKey = `SYSTEM\CurrentControlSet\Control\Network\{4D36E972-E325-11CE-BFC1-08002BE10318}`
	netCfgInstanceIDValue = "NetCfgInstanceId"
	connectionKey         = "Connection"
	connectionNameValue   = "Name"
)

// netClassGUID is the device setup class of network adapters.
var netClassGUID = windows.GUID{
	Data1: 0x4d36e972, Data2: 0xe325, Data3: 0x11ce,
	Data4: [8]byte{0xbf, 0xc1, 0x08, 0x00, 0x2b, 0xe1, 0x03, 0x18},
}

// wintunAdapter is one wintun adapter in the device list.
type wintunAdapter struct {
	// name is the name the network settings show.
	name string
	// instanceID is the GUID of the adapter's network, the one the creator
	// requested, as "{...}".
	instanceID string
	devices    windows.DevInfo
	device     *windows.DevInfoData
}

// eachWintunAdapter calls visit for every wintun adapter the system knows,
// including ones that are not present any more. A device that cannot be read
// is not listed; a listing that breaks off is the error.
func eachWintunAdapter(visit func(wintunAdapter)) error {
	return eachNetDevice(func(devices windows.DevInfo, device *windows.DevInfoData) {
		if adapter, ok := readWintunAdapter(devices, device); ok {
			visit(adapter)
		}
	})
}

// eachNetDevice calls visit for every network device of the system, present or
// not.
func eachNetDevice(visit func(windows.DevInfo, *windows.DevInfoData)) error {
	devices, err := windows.SetupDiGetClassDevsEx(&netClassGUID, "", 0, 0, 0, "")
	if err != nil {
		return fmt.Errorf("list network adapters: %w", err)
	}
	defer devices.Close()

	for index := 0; ; index++ {
		device, err := devices.EnumDeviceInfo(index)
		if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("list network adapters: %w", err)
		}
		visit(devices, device)
	}
}

// listWintunAdapters lists all wintun adapters, for diagnostics and tests.
func listWintunAdapters() ([]wintunAdapter, error) {
	var adapters []wintunAdapter
	err := eachWintunAdapter(func(adapter wintunAdapter) { adapters = append(adapters, adapter) })
	return adapters, err
}

// removeStaleAdapters removes the wintun adapters whose name starts with
// prefix and returns their names. A daemon that is killed does not close its
// adapters; wintun's software device normally dies with the process, and this
// is the net under that for the cases where it does not. It removes whatever
// has the prefix, so it must run before the process creates its own adapters,
// and prefix must be one nobody else's adapter has.
//
// It needs an elevated process. A failure does not stop it: each adapter that
// can be removed is, and the errors are joined.
func removeStaleAdapters(prefix string) (removed []string, err error) {
	if prefix == "" {
		return nil, errors.New("refusing to remove adapters without a name prefix")
	}
	var failures []error
	listErr := eachWintunAdapter(func(adapter wintunAdapter) {
		if !hasNamePrefix(adapter.name, prefix) {
			return
		}
		if err := removeDevice(adapter.devices, adapter.device); err != nil {
			failures = append(failures, fmt.Errorf("remove adapter %s: %w", adapter.name, err))
			return
		}
		removed = append(removed, adapter.name)
	})
	return removed, errors.Join(append(failures, listErr)...)
}

// readWintunAdapter returns the device as a wintun adapter, if it is one.
func readWintunAdapter(devices windows.DevInfo, device *windows.DevInfoData) (wintunAdapter, bool) {
	ids, err := devices.DeviceRegistryProperty(device, windows.SPDRP_HARDWAREID)
	hardwareIDs, isList := ids.([]string)
	if err != nil || !isList || !hasWintunID(hardwareIDs) {
		return wintunAdapter{}, false
	}
	instanceID, err := netCfgInstanceID(devices, device)
	if err != nil {
		return wintunAdapter{}, false
	}
	name, err := connectionName(instanceID)
	if err != nil {
		return wintunAdapter{}, false
	}
	return wintunAdapter{name: name, instanceID: instanceID, devices: devices, device: device}, true
}

func hasWintunID(hardwareIDs []string) bool {
	return slices.ContainsFunc(hardwareIDs, func(id string) bool {
		return strings.EqualFold(id, wintunHardwareID)
	})
}

// hasNamePrefix compares as Windows compares adapter names: without regard to
// case.
func hasNamePrefix(name, prefix string) bool {
	return len(name) >= len(prefix) && strings.EqualFold(name[:len(prefix)], prefix)
}

// netCfgInstanceID reads the GUID that names the adapter in the network
// settings of the registry.
func netCfgInstanceID(devices windows.DevInfo, device *windows.DevInfoData) (string, error) {
	driverKey, err := devices.OpenDevRegKey(device, windows.DICS_FLAG_GLOBAL, 0, windows.DIREG_DRV, registry.QUERY_VALUE)
	if err != nil {
		return "", fmt.Errorf("open the driver key: %w", err)
	}
	key := registry.Key(driverKey)
	defer key.Close()
	instanceID, _, err := key.GetStringValue(netCfgInstanceIDValue)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", netCfgInstanceIDValue, err)
	}
	return instanceID, nil
}

// connectionName reads the name of an adapter as the network settings show it.
func connectionName(instanceID string) (string, error) {
	connection, err := registry.OpenKey(registry.LOCAL_MACHINE,
		networkConnectionsKey+`\`+instanceID+`\`+connectionKey, registry.QUERY_VALUE)
	if err != nil {
		return "", fmt.Errorf("open the connection key: %w", err)
	}
	defer connection.Close()
	name, _, err := connection.GetStringValue(connectionNameValue)
	if err != nil {
		return "", fmt.Errorf("read the connection name: %w", err)
	}
	return name, nil
}

// removeDevice removes the device from the system, as Device Manager does.
func removeDevice(devices windows.DevInfo, device *windows.DevInfoData) error {
	params := windows.RemoveDeviceParams{
		ClassInstallHeader: *windows.MakeClassInstallHeader(windows.DIF_REMOVE),
		Scope:              windows.DI_REMOVEDEVICE_GLOBAL,
	}
	if err := devices.SetClassInstallParams(device, &params.ClassInstallHeader, uint32(unsafe.Sizeof(params))); err != nil {
		return err
	}
	return devices.CallClassInstaller(windows.DIF_REMOVE, device)
}
