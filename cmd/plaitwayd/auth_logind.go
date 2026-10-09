package main

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// logindSeatsDir is where systemd-logind keeps one file for each seat.
const logindSeatsDir = "/run/systemd/seats"

// seatActiveUIDKey is the line of a seat file that names the user of the
// session that is active on the seat (sd_seat_get_active reads it as
// ACTIVE_UID). A seat nobody is logged in at has none.
const seatActiveUIDKey = "ACTIVE_UID"

// seatUsers returns the users with an active session on a seat of this
// machine, which are the users at the console: the one at seat0's screen and
// whoever is active on any other seat. Sessions that come in over the network
// have no seat and are never among them.
//
// It fails when logind's state is not there or cannot be read, and the policy
// then authorizes administrators only.
func seatUsers(dir string) ([]uint32, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("systemd-logind has no seat state: %s does not exist", dir)
	}
	if err != nil {
		return nil, fmt.Errorf("read the seats of systemd-logind: %w", err)
	}
	var uids []uint32
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		uid, active, err := seatActiveUID(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("seat %s: %w", entry.Name(), err)
		}
		if active {
			uids = append(uids, uid)
		}
	}
	return uids, nil
}

// seatActiveUID reads one seat file: key=value lines, comments start with #.
func seatActiveUID(path string) (uid uint32, active bool, err error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, false, nil // the seat went away since the directory was read
	}
	if err != nil {
		return 0, false, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, value, found := strings.Cut(strings.TrimSpace(scanner.Text()), "=")
		if !found || key != seatActiveUIDKey {
			continue
		}
		uid, err := parseID(value)
		if err != nil {
			return 0, false, fmt.Errorf("%s %q is not a user id", seatActiveUIDKey, value)
		}
		return uid, true, nil
	}
	return 0, false, scanner.Err()
}

// refreshed returns read, called at most once per consoleRefresh: the answer,
// a failure too, is reused in between, because it is needed on every call and
// changes only at login and logout.
func refreshed[T any](read func() (T, error), now func() time.Time) func() (T, error) {
	var (
		mu     sync.Mutex
		readAt time.Time
		value  T
		err    error
	)
	return func() (T, error) {
		mu.Lock()
		defer mu.Unlock()
		if t := now(); readAt.IsZero() || t.Sub(readAt) >= consoleRefresh {
			value, err = read()
			readAt = t
		}
		return value, err
	}
}

// linuxPolicy is the policy of a Linux host: administrators are root, the
// members of the administrators' groups and, for a development daemon, its own
// user; the users at the console are the active users of the seats of logind.
func linuxPolicy(accounts accountDatabase, seatsDir string, daemonUID *uint32) *policy {
	return &policy{
		administrator:  administrators{accounts: accounts, daemonUID: daemonUID}.is,
		consoleUIDs:    refreshed(func() ([]uint32, error) { return seatUsers(seatsDir) }, time.Now),
		consoleSession: activeConsoleSession,
	}
}
