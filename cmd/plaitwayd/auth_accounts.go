package main

import (
	"errors"
	"fmt"
	"os/user"
	"slices"
	"strconv"

	"github.com/KoukeNeko/Plaitway/internal/peercred"
)

// adminGroupNames are the groups whose members administer a Linux machine:
// sudo on Debian and Ubuntu, wheel on Fedora, Arch, RHEL and SUSE, admin on
// older Ubuntu. A host has one or two of them.
var adminGroupNames = []string{"sudo", "wheel", "admin"}

// accountDatabase is what the Linux policy reads of the account database: the
// system's in production, a fake one in the tests.
type accountDatabase struct {
	// groupID returns the gid of a group. ok is false for a group the host does
	// not have.
	groupID func(name string) (gid uint32, ok bool, err error)
	// groupIDs returns the groups of the account with uid, its primary group
	// included. An account the database does not know has none.
	groupIDs func(uid uint32) ([]uint32, error)
}

// systemAccounts reads the account database through os/user, which consults
// the name service switch when the daemon is built with cgo and /etc/passwd and
// /etc/group otherwise.
func systemAccounts() accountDatabase {
	return accountDatabase{
		groupID: func(name string) (uint32, bool, error) {
			g, err := user.LookupGroup(name)
			var unknown user.UnknownGroupError
			if errors.As(err, &unknown) {
				return 0, false, nil
			}
			if err != nil {
				return 0, false, err
			}
			gid, err := parseID(g.Gid)
			return gid, err == nil, err
		},
		groupIDs: func(uid uint32) ([]uint32, error) {
			u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
			var unknown user.UnknownUserIdError
			if errors.As(err, &unknown) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			names, err := u.GroupIds()
			if err != nil {
				return nil, err
			}
			ids := make([]uint32, len(names))
			for i, name := range names {
				if ids[i], err = parseID(name); err != nil {
					return nil, err
				}
			}
			return ids, nil
		},
	}
}

func parseID(s string) (uint32, error) {
	id, err := strconv.ParseUint(s, 10, 32)
	return uint32(id), err
}

// administrators decides who is an administrator on Linux, besides root.
type administrators struct {
	accounts accountDatabase
	// daemonUID is the user a development daemon runs as, who is its
	// administrator: nobody else could change a profile of a daemon that is not
	// root. It is nil for a privileged daemon.
	daemonUID *uint32
}

// is looks at the groups of the account behind the caller's uid in the account
// database, because SO_PEERCRED reports only the primary group of the process;
// that one counts as well.
func (a administrators) is(ai peercred.AuthInfo) (bool, error) {
	if a.daemonUID != nil && ai.UID == *a.daemonUID {
		return true, nil
	}
	var admin []uint32
	var errs []error
	for _, name := range adminGroupNames {
		gid, ok, err := a.accounts.groupID(name)
		if err != nil {
			errs = append(errs, fmt.Errorf("look up group %s: %w", name, err))
		} else if ok {
			admin = append(admin, gid)
		}
	}
	member := func(gid uint32) bool { return slices.Contains(admin, gid) }
	if slices.ContainsFunc(ai.Groups, member) {
		return true, nil
	}
	groups, err := a.accounts.groupIDs(ai.UID)
	if err != nil {
		errs = append(errs, fmt.Errorf("read the groups of uid %d: %w", ai.UID, err))
	}
	if slices.ContainsFunc(groups, member) {
		return true, nil
	}
	return false, errors.Join(errs...)
}
