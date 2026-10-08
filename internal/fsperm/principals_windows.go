//go:build windows

package fsperm

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// principals are the accounts that may own or reach a private object.
type principals struct {
	system, administrators, user *windows.SID
	// elevated is true when the process already reaches everything through the
	// Administrators group (or is SYSTEM, which is a member of it).
	elevated bool
}

func currentPrincipals() (principals, error) {
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return principals{}, fmt.Errorf("look up the SYSTEM account: %w", err)
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return principals{}, fmt.Errorf("look up the Administrators group: %w", err)
	}
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return principals{}, fmt.Errorf("look up the account of the process: %w", err)
	}
	// A null token asks about the thread's effective token: CheckTokenMembership
	// refuses a primary token handle.
	elevated, err := windows.Token(0).IsMember(administrators)
	if err != nil {
		return principals{}, fmt.Errorf("look up the groups of the process: %w", err)
	}
	return principals{system: system, administrators: administrators, user: tokenUser.User.Sid, elevated: elevated}, nil
}

// trusts reports whether sid may own a private object and be named in its list.
func (p principals) trusts(sid *windows.SID) bool {
	return sid != nil && (sid.Equals(p.system) || sid.Equals(p.administrators) || sid.Equals(p.user))
}

// grantees are the accounts a new access list names. A development daemon is
// not elevated and would lock itself out of its own directories otherwise.
func (p principals) grantees() []*windows.SID {
	grantees := []*windows.SID{p.system, p.administrators}
	if !p.elevated && !p.user.Equals(p.system) {
		grantees = append(grantees, p.user)
	}
	return grantees
}
