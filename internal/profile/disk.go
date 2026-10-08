package profile

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// The on-disk form uses names instead of the numeric tunnel enums, so that
// reordering a constant elsewhere cannot change what a stored profile means.
type diskIndex struct {
	Schema   int           `json:"schema"`
	Profiles []diskProfile `json:"profiles"`
}

// ExcludePrivateIPs and OnDemand were added to schema 1 later: an index written
// without them reads with both off.
type diskProfile struct {
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	Kind              string         `json:"kind"`
	AutoConnect       bool           `json:"auto_connect"`
	TunnelMode        string         `json:"tunnel_mode"`
	Priority          int            `json:"priority"`
	ExcludePrivateIPs bool           `json:"exclude_private_ips"`
	OnDemand          OnDemand       `json:"on_demand"`
	Imported          time.Time      `json:"imported"`
	Summary           tunnel.Summary `json:"summary"`
}

var kindNames = map[tunnel.Kind]string{
	tunnel.KindOpenVPN:   "openvpn",
	tunnel.KindWireGuard: "wireguard",
}

var modeNames = map[tunnel.Mode]string{
	tunnel.ModeAuto:  "auto",
	tunnel.ModeFull:  "full",
	tunnel.ModeSplit: "split",
}

func toDisk(p Profile) diskProfile {
	return diskProfile{
		ID:                p.ID,
		Name:              p.Name,
		Kind:              kindNames[p.Kind],
		AutoConnect:       p.Settings.AutoConnect,
		TunnelMode:        modeNames[p.Settings.TunnelMode],
		Priority:          p.Settings.Priority,
		ExcludePrivateIPs: p.Settings.ExcludePrivateIPs,
		OnDemand:          p.Settings.OnDemand,
		Imported:          p.Imported,
		Summary:           p.Summary,
	}
}

// fromDisk checks the record: its id becomes part of a file name, so a damaged
// or tampered index must not be able to name a path.
func fromDisk(d diskProfile) (Profile, error) {
	if !validID(d.ID) {
		return Profile{}, fmt.Errorf("invalid profile id %q", d.ID)
	}
	kind, ok := keyOf(kindNames, d.Kind)
	if !ok {
		return Profile{}, fmt.Errorf("profile %s has unknown kind %q", d.ID, d.Kind)
	}
	mode, ok := keyOf(modeNames, d.TunnelMode)
	if !ok {
		return Profile{}, fmt.Errorf("profile %s has unknown tunnel mode %q", d.ID, d.TunnelMode)
	}
	if d.Priority < 1 {
		return Profile{}, fmt.Errorf("profile %s has priority %d", d.ID, d.Priority)
	}
	return Profile{
		ID:   d.ID,
		Name: d.Name,
		Kind: kind,
		Settings: Settings{
			AutoConnect:       d.AutoConnect,
			TunnelMode:        mode,
			Priority:          d.Priority,
			ExcludePrivateIPs: d.ExcludePrivateIPs,
			OnDemand:          d.OnDemand,
		},
		Imported: d.Imported,
		Summary:  d.Summary,
	}, nil
}

func keyOf[K comparable](names map[K]string, name string) (K, bool) {
	for k, v := range names {
		if v == name {
			return k, true
		}
	}
	var zero K
	return zero, false
}

// validID accepts exactly what newID produces.
func validID(id string) bool {
	if len(id) != 26 {
		return false
	}
	for _, c := range id {
		if (c < 'A' || c > 'Z') && (c < '2' || c > '7') {
			return false
		}
	}
	return true
}

// readRegular reads a file the daemon wrote itself, and refuses anything that
// is not a regular file (a symlink planted in the state directory, a device)
// or is larger than limit.
func readRegular(path string, limit int64) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, limit)
	}
	return data, nil
}

// writeFileAtomic replaces path with data (mode 0600): it writes a temporary
// file next to it, syncs it and renames it over path (replaceFile), so a crash
// leaves either the old or the new file.
func writeFileAtomic(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, tmpPrefix+"*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tmp.Close()
			if rmErr := os.Remove(tmp.Name()); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
				err = errors.Join(err, rmErr)
			}
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return replaceFile(tmp.Name(), path)
}
