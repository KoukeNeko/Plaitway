package main

import (
	"errors"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	// connectAndCreate is the least access to the Service Control Manager that
	// install needs. The control commands need no more than it.
	connectAndCreate = windows.SC_MANAGER_CONNECT | windows.SC_MANAGER_CREATE_SERVICE
)

// notInstalledIfMissing turns the error for a name the SCM does not know into
// errNotInstalled.
func notInstalledIfMissing(err error) error {
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return errNotInstalled
	}
	return err
}

// systemSCM is the Service Control Manager of this machine.
type systemSCM struct {
	manager *mgr.Mgr
}

func connectSCM() (serviceControl, error) {
	handle, err := windows.OpenSCManager(nil, nil, connectAndCreate)
	if err != nil {
		return nil, fmt.Errorf("connect to the Service Control Manager: %w", err)
	}
	return systemSCM{manager: &mgr.Mgr{Handle: handle}}, nil
}

func (s systemSCM) Open(name string) (registeredService, error) {
	opened, err := s.manager.OpenService(name)
	if err != nil {
		return nil, notInstalledIfMissing(err)
	}
	return systemService{service: opened}, nil
}

// Create registers name with a minimal configuration; Apply sets the rest, so
// that a new service and an updated one get exactly the same settings.
func (s systemSCM) Create(name, executable string) (registeredService, error) {
	created, err := s.manager.CreateService(name, executable, mgr.Config{StartType: mgr.StartManual})
	if err != nil {
		return nil, err
	}
	return systemService{service: created}, nil
}

func (s systemSCM) Close() error { return s.manager.Disconnect() }

// queryServiceStatus reads the state of a service with the right to read it
// and no other, which is all an unelevated caller has.
func queryServiceStatus(name string) (svc.Status, error) {
	manager, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return svc.Status{}, fmt.Errorf("connect to the Service Control Manager: %w", err)
	}
	defer windows.CloseServiceHandle(manager)
	namePointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return svc.Status{}, err
	}
	handle, err := windows.OpenService(manager, namePointer, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return svc.Status{}, notInstalledIfMissing(err)
	}
	service := &mgr.Service{Name: name, Handle: handle}
	defer service.Close()
	return service.Query()
}

// systemService is a service registered with the Service Control Manager.
type systemService struct {
	service *mgr.Service
}

func (s systemService) Status() (svc.Status, error) { return s.service.Query() }
func (s systemService) Start() error                { return s.service.Start() }
func (s systemService) Delete() error               { return s.service.Delete() }
func (s systemService) Close() error                { return s.service.Close() }

func (s systemService) Stop() error {
	_, err := s.service.Control(svc.Stop)
	return err
}

// Settings reads what Apply sets.
func (s systemService) Settings() (serviceSettings, error) {
	config, err := s.service.Config()
	if err != nil {
		return serviceSettings{}, fmt.Errorf("read the configuration: %w", err)
	}
	recovery, err := s.service.RecoveryActions()
	if err != nil {
		return serviceSettings{}, fmt.Errorf("read the failure actions: %w", err)
	}
	resetSeconds, err := s.service.ResetPeriod()
	if err != nil {
		return serviceSettings{}, fmt.Errorf("read the failure count reset: %w", err)
	}
	onNonCrash, err := s.service.RecoveryActionsOnNonCrashFailures()
	if err != nil {
		return serviceSettings{}, fmt.Errorf("read the failure actions flag: %w", err)
	}
	accessList, err := s.readAccessList()
	if err != nil {
		return serviceSettings{}, err
	}
	timeout, err := s.readPreshutdownTimeout()
	if err != nil {
		return serviceSettings{}, err
	}
	return serviceSettings{
		config:             config,
		recovery:           recovery,
		resetPeriod:        time.Duration(resetSeconds) * time.Second,
		restartOnNonCrash:  onNonCrash,
		accessList:         accessList,
		preshutdownTimeout: timeout,
	}, nil
}

// Apply sets every part of the settings. The access list comes last: an
// administrator who is locked out by a mistake in it could not set the rest.
func (s systemService) Apply(settings serviceSettings) error {
	if err := s.service.UpdateConfig(settings.config); err != nil {
		return fmt.Errorf("set the configuration: %w", err)
	}
	if err := s.applyFailureActions(settings); err != nil {
		return err
	}
	if err := s.writePreshutdownTimeout(settings.preshutdownTimeout); err != nil {
		return err
	}
	return s.writeAccessList(settings.accessList)
}

func (s systemService) applyFailureActions(settings serviceSettings) error {
	// A service that had none must be able to go back to none.
	if len(settings.recovery) == 0 {
		if err := s.service.ResetRecoveryActions(); err != nil {
			return fmt.Errorf("clear the failure actions: %w", err)
		}
	} else if err := s.service.SetRecoveryActions(settings.recovery, uint32(settings.resetPeriod/time.Second)); err != nil {
		return fmt.Errorf("set the failure actions: %w", err)
	}
	if err := s.service.SetRecoveryActionsOnNonCrashFailures(settings.restartOnNonCrash); err != nil {
		return fmt.Errorf("set the failure actions flag: %w", err)
	}
	return nil
}

// preshutdownInfo is SERVICE_PRESHUTDOWN_INFO.
type preshutdownInfo struct {
	timeoutMilliseconds uint32
}

func (s systemService) readPreshutdownTimeout() (time.Duration, error) {
	var info preshutdownInfo
	var needed uint32
	err := windows.QueryServiceConfig2(s.service.Handle, windows.SERVICE_CONFIG_PRESHUTDOWN_INFO,
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), &needed)
	if err != nil {
		return 0, fmt.Errorf("read the preshutdown timeout: %w", err)
	}
	return time.Duration(info.timeoutMilliseconds) * time.Millisecond, nil
}

func (s systemService) writePreshutdownTimeout(timeout time.Duration) error {
	info := preshutdownInfo{timeoutMilliseconds: uint32(timeout.Milliseconds())}
	err := windows.ChangeServiceConfig2(s.service.Handle, windows.SERVICE_CONFIG_PRESHUTDOWN_INFO, (*byte)(unsafe.Pointer(&info)))
	if err != nil {
		return fmt.Errorf("set the preshutdown timeout: %w", err)
	}
	return nil
}

func (s systemService) readAccessList() (string, error) {
	sd, err := windows.GetSecurityInfo(s.service.Handle, windows.SE_SERVICE, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return "", fmt.Errorf("read the access list: %w", err)
	}
	return sd.String(), nil
}

func (s systemService) writeAccessList(sddl string) error {
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("read the access list %q: %w", sddl, err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("read the access list %q: %w", sddl, err)
	}
	err = windows.SetSecurityInfo(s.service.Handle, windows.SE_SERVICE,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
	if err != nil {
		return fmt.Errorf("set the access list: %w", err)
	}
	return nil
}
