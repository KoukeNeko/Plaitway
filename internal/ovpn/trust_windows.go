package ovpn

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"github.com/KoukeNeko/Plaitway/internal/authenticode"
	"github.com/KoukeNeko/Plaitway/internal/fsperm"
)

const (
	// openvpnSigner is the publisher of the official OpenVPN builds.
	openvpnSigner = "OpenVPN Inc."
	// tapctlName is the program that makes and removes TAP adapters. It comes
	// with OpenVPN and is trusted on the same terms.
	tapctlName = "tapctl.exe"
	// tapDriverFile is what the TAP-Windows6 driver puts in the system.
	tapDriverFile = "tap0901.sys"
	// windowsDriverName is the driver the engine runs tunnels on, as OpenVPN
	// calls it (--show-adapters).
	windowsDriverName = "tap-windows6"
)

// The daemon runs openvpn as LocalSystem, so a file that a standard user can
// replace must never be run. The official installer puts OpenVPN in Program
// Files, where only administrators write; the checks here hold the installation
// to that, and do not take the path for granted:
//
//   - the file and every directory above it belong to and can be changed by
//     SYSTEM, Administrators and TrustedInstaller only (fsperm), which includes
//     the libraries openvpn loads from its own directory, since a directory
//     that only they can write holds only what they put there;
//   - the file carries a valid Authenticode signature of OpenVPN Inc.;
//   - when the daemon was built for one binary, its SHA-256 is that one.
//
// The file is opened without write sharing before the checks and stays open
// until the process has started, so it cannot be swapped in between. It is not
// copied: a copy of openvpn.exe alone does not run, since the libraries sit
// beside it, and a copy of all of them into a protected directory would be the
// administrators' directory over again.

// VerifyBinary reports why the daemon would not run the openvpn at path as
// LocalSystem, or nil when it would. wantSHA256 (hex) pins the file when not
// empty. The engine makes the same check before each use of the binary; this is
// for the daemon to say so early.
func VerifyBinary(path, wantSHA256 string) error {
	release, err := lockTrusted(path, wantSHA256)
	if err != nil {
		return err
	}
	release()
	return nil
}

// lockTrusted checks the file at path (see above) and returns a release that
// ends the lock.
func lockTrusted(path, wantSHA256 string) (release func(), err error) {
	if path == "" {
		return nil, errors.New("openvpn binary is not configured")
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("openvpn binary %q is not an absolute path; a name would be looked up in directories anyone may write", path)
	}
	file, err := openLocked(path)
	if err != nil {
		return nil, err
	}
	if err := checkLocked(file, path, wantSHA256); err != nil {
		file.Close()
		return nil, err
	}
	return func() { file.Close() }, nil
}

// openLocked opens path for reading, shared with readers only: nobody can
// write to or delete it while the file is open.
func openLocked(path string) (*os.File, error) {
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("openvpn binary %q: %w", path, err)
	}
	handle, err := windows.CreateFile(path16, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return nil, fmt.Errorf("openvpn not found at %s", path)
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return os.NewFile(uintptr(handle), path), nil
}

func checkLocked(file *os.File, path, wantSHA256 string) error {
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if err := fsperm.CheckAdminOnlyPath(path); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := authenticode.VerifySignedBy(windows.Handle(file.Fd()), path, openvpnSigner); err != nil {
		return fmt.Errorf("%s %w", path, err)
	}
	if wantSHA256 == "" {
		return nil
	}
	return checkSHA256(file, path, wantSHA256)
}

func checkSHA256(file *os.File, path, want string) error {
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != strings.ToLower(want) {
		return fmt.Errorf("%s is not the binary this daemon was built for (sha256 %s, expected %s)", path, got, want)
	}
	return nil
}

// tapctlPath is the tapctl.exe beside the openvpn binary.
func tapctlPath(binary string) string { return filepath.Join(filepath.Dir(binary), tapctlName) }

// inspectBinary asks the binary what it is, after the checks of lockTrusted and
// with the file locked while it runs, and checks what the engine needs besides.
func inspectBinary(cfg Config, timeout time.Duration) binaryInfo {
	release, err := lockTrusted(cfg.Binary, cfg.BinarySHA256)
	if err != nil {
		return binaryInfo{detail: "openvpn is not trusted: " + err.Error()}
	}
	defer release()
	info := probeBinary(cfg.Binary, timeout)
	if !info.available {
		return info
	}
	if err := checkAdapterTooling(cfg.Binary); err != nil {
		return binaryInfo{detail: err.Error()}
	}
	info.driver = windowsDriverName
	return info
}

// checkAdapterTooling confirms what the engine needs to give openvpn an
// adapter: the TAP-Windows6 driver and a trusted tapctl.exe.
func checkAdapterTooling(binary string) error {
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return fmt.Errorf("find the system directory: %w", err)
	}
	driver := filepath.Join(system, "drivers", tapDriverFile)
	if _, err := os.Stat(driver); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("the TAP-Windows6 driver is not installed (%s is missing); install OpenVPN with its TAP-Windows6 driver", driver)
	} else if err != nil {
		return fmt.Errorf("look for the TAP-Windows6 driver: %w", err)
	}
	release, err := lockTrusted(tapctlPath(binary), "")
	if err != nil {
		return fmt.Errorf("tapctl is not trusted: %w", err)
	}
	release()
	return nil
}

// trustBinary is the check before a start. The caller keeps the lock until the
// process runs.
func trustBinary(cfg Config) (release func(), err error) {
	return lockTrusted(cfg.Binary, cfg.BinarySHA256)
}
