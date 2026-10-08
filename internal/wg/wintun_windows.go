package wg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wintun"

	"github.com/KoukeNeko/Plaitway/internal/authenticode"
)

const (
	wintunDLLName = "wintun.dll"
	// wintunSigner is who signs wintun.dll; the same name is checked by
	// packaging/windows/fetch-wintun.ps1 before it hands the file over.
	wintunSigner = "WireGuard LLC"
	// wintunEntryPoint is looked up to prove that the file is a usable wintun.
	wintunEntryPoint = "WintunCreateAdapter"

	// The DLL is given by its full path, so only what it depends on is
	// searched for: the system directory, and its own folder.
	wintunLoadFlags = windows.LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR | windows.LOAD_LIBRARY_SEARCH_SYSTEM32
)

// loadedWintun is the module of the wintun.dll that was checked and loaded.
var loadedWintun struct {
	mu     sync.Mutex
	module windows.Handle
}

// executableDir is where wintun.dll must be: next to the program, as the
// installer puts it. A test replaces it.
var executableDir = func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

// ensureWintun loads wintun.dll from the folder of the executable after
// checking its signature, once. golang.zx2c4.com/wintun, which wireguard-go
// calls, loads "wintun.dll" by name from the application directory and
// system32, never from the current directory or PATH; that call finds the
// module loaded here, because the loader reuses a loaded module of the same
// name. Loading it first, by full path and checked, means the file that
// wireguard-go uses is the file that was verified.
func ensureWintun() error {
	loadedWintun.mu.Lock()
	defer loadedWintun.mu.Unlock()
	if loadedWintun.module != 0 {
		return nil
	}
	dir, err := executableDir()
	if err != nil {
		return fmt.Errorf("find the folder of the executable: %w", err)
	}
	module, err := loadVerifiedLibrary(filepath.Join(dir, wintunDLLName))
	if err != nil {
		return err
	}
	loadedWintun.module = module
	return nil
}

func wintunLoaded() bool {
	loadedWintun.mu.Lock()
	defer loadedWintun.mu.Unlock()
	return loadedWintun.module != 0
}

// loadVerifiedLibrary loads the wintun.dll at path if it is a file signed by
// wintunSigner. The file is held open without write sharing from the check to
// the load, so that it cannot be swapped in between.
func loadVerifiedLibrary(path string) (windows.Handle, error) {
	file, err := openRegularFile(path)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(file)

	if err := authenticode.VerifySignedBy(file, path, wintunSigner); err != nil {
		return 0, fmt.Errorf("%s %w", path, err)
	}
	module, err := windows.LoadLibraryEx(path, 0, wintunLoadFlags)
	if err != nil {
		return 0, fmt.Errorf("load %s: %w", path, err)
	}
	if err := checkLoadedModule(module, path); err != nil {
		windows.FreeLibrary(module)
		return 0, err
	}
	return module, nil
}

// checkLoadedModule proves that module is a wintun and that the lookup by name
// that wireguard-go makes finds this module and no other.
func checkLoadedModule(module windows.Handle, path string) error {
	if _, err := windows.GetProcAddress(module, wintunEntryPoint); err != nil {
		return fmt.Errorf("%s is not a usable wintun: %w", path, err)
	}
	name16, err := windows.UTF16PtrFromString(wintunDLLName)
	if err != nil {
		return err
	}
	var byName windows.Handle
	err = windows.GetModuleHandleEx(windows.GET_MODULE_HANDLE_EX_FLAG_UNCHANGED_REFCOUNT, name16, &byName)
	if err != nil || byName != module {
		return fmt.Errorf("%s is loaded, but the process already has another %s", path, wintunDLLName)
	}
	return nil
}

// openRegularFile opens path for reading without sharing it for writing, and
// refuses what is not a plain file.
func openRegularFile(path string) (windows.Handle, error) {
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	file, err := windows.CreateFile(path16, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return 0, fmt.Errorf("%s is missing; the WireGuard engine needs wintun.dll in the folder of the program", path)
	}
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(file, &info); err != nil {
		windows.CloseHandle(file)
		return 0, fmt.Errorf("read %s: %w", path, err)
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		windows.CloseHandle(file)
		return 0, fmt.Errorf("%s is a folder or a link, not a file", path)
	}
	return file, nil
}

// wintunVersion is the version of the loaded wintun.dll; the driver inside it
// is installed by the first adapter.
func wintunVersion() string {
	if !wintunLoaded() {
		return ""
	}
	return wintun.Version()
}
