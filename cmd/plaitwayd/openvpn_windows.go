package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

const (
	openvpnFileName = "openvpn.exe"
	// openvpnBinFolder is the folder of openvpn.exe in an OpenVPN installation,
	// which holds tapctl.exe and the libraries beside it.
	openvpnBinFolder = "bin"
	// bundledOpenVPNFolder is where the installer of Plaitway puts its own copy
	// of OpenVPN, next to plaitwayd.exe: <folder of plaitwayd.exe>\openvpn\bin.
	bundledOpenVPNFolder = "openvpn"
	// installedOpenVPNFolder is where the official installer puts OpenVPN,
	// below Program Files.
	installedOpenVPNFolder = "OpenVPN"
)

// defaultOpenVPN is the openvpn.exe the daemon uses when -openvpn is not given:
// the copy that is bundled next to plaitwayd.exe, else the standard
// installation of OpenVPN.
//
// The folder comes from the shell, not from %ProgramFiles%: a service's
// environment is not something a LocalSystem process should take a program path
// from. This only chooses a path; whether the daemon would run it is the
// engine's verdict (see trustedOpenVPN).
func defaultOpenVPN() string {
	var executableDir, programFiles string
	if exe, err := os.Executable(); err == nil {
		executableDir = filepath.Dir(exe)
	}
	// Without Program Files the bundled path is all there is to name: the
	// engine then reports that openvpn is not found there, and the daemon
	// starts regardless.
	if folder, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, windows.KF_FLAG_DEFAULT); err == nil {
		programFiles = folder
	}
	return chooseOpenVPN(executableDir, programFiles, openvpnMayExist)
}

func bundledOpenVPNPath(executableDir string) string {
	return filepath.Join(executableDir, bundledOpenVPNFolder, openvpnBinFolder, openvpnFileName)
}

func installedOpenVPNPath(programFiles string) string {
	return filepath.Join(programFiles, installedOpenVPNFolder, openvpnBinFolder, openvpnFileName)
}

// chooseOpenVPN is the first of the bundled copy and the standard installation
// that exists. A folder that is not known leaves its place out. When none
// exists it is the standard installation, so that the reason the engine gives
// names the place where an administrator would install OpenVPN.
func chooseOpenVPN(executableDir, programFiles string, exists func(path string) bool) string {
	var candidates []string
	if executableDir != "" {
		candidates = append(candidates, bundledOpenVPNPath(executableDir))
	}
	if programFiles != "" {
		candidates = append(candidates, installedOpenVPNPath(programFiles))
	}
	for _, candidate := range candidates {
		if exists(candidate) {
			return candidate
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	return candidates[len(candidates)-1]
}

// openvpnMayExist is false only for a path that is not there. A path that
// cannot be looked at is taken, so that the engine reports why instead of the
// daemon silently choosing another binary.
func openvpnMayExist(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, fs.ErrNotExist)
}

// trustedOpenVPN gives the engine the configured path and passes no verdict.
// Unlike on macOS the binary is not copied: openvpn.exe loads libraries from its
// own folder, so the engine checks it where it is (owner and access lists of the
// file and every folder above it, the OpenVPN Inc. signature and, when this
// build has one, the SHA-256 in openvpnSHA256) before every probe and every
// start, and keeps the file locked until the process runs. The verdict stays
// live: an OpenVPN installed after the daemon started is used without a restart.
func trustedOpenVPN(source, _ string) (string, error) {
	return source, nil
}
