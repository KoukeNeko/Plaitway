package main

import (
	"errors"
	"os"
	"os/user"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/peercred"
)

const (
	sudoGID  = 27
	wheelGID = 10
	adminGrp = 115
	// seatsAbsent is the directory of a machine without systemd-logind.
	seatsAbsent = "/nonexistent/plaitway-test/seats"
)

// accountsOf is an account database of a host that has the named groups, and
// whose accounts are in the groups listed for them (the primary group
// included, as os/user reports it).
func accountsOf(groups map[string]uint32, accounts map[uint32][]uint32) accountDatabase {
	return accountDatabase{
		groupID: func(name string) (uint32, bool, error) {
			gid, ok := groups[name]
			return gid, ok, nil
		},
		groupIDs: func(uid uint32) ([]uint32, error) { return accounts[uid], nil },
	}
}

func debianAccounts() accountDatabase {
	return accountsOf(map[string]uint32{"sudo": sudoGID}, map[uint32][]uint32{
		1000: {1000, sudoGID, 100},
		1001: {1001, 100},
		1002: {sudoGID}, // sudo is its primary group
	})
}

func TestLinuxAdministrators(t *testing.T) {
	fedora := accountsOf(map[string]uint32{"wheel": wheelGID}, map[uint32][]uint32{
		1000: {1000, wheelGID},
		1001: {1001},
		1002: {wheelGID},
	})
	ubuntuLTS := accountsOf(map[string]uint32{"admin": adminGrp, "sudo": sudoGID}, map[uint32][]uint32{
		1000: {1000, adminGrp},
		1001: {1001, 100},
	})
	own := uint32(1001)
	for _, tt := range []struct {
		name      string
		accounts  accountDatabase
		who       peercred.AuthInfo
		daemonUID *uint32
		want      bool
	}{
		{"member of sudo", debianAccounts(), identity(1000, 1000), nil, true},
		{"member of wheel", fedora, identity(1000, 1000), nil, true},
		{"member of admin", ubuntuLTS, identity(1000, 1000), nil, true},
		{"not a member of any", debianAccounts(), identity(1001, 1001), nil, false},
		{"sudo is its primary group", debianAccounts(), identity(1002, sudoGID), nil, true},
		{"wheel is its primary group in the account database", fedora, identity(1002, 1002), nil, true},
		// SO_PEERCRED reports the primary group of the process; an account the
		// database does not know can still be in a group by it.
		{"administrator group reported by the kernel only", debianAccounts(), identity(2000, sudoGID), nil, true},
		{"account the database does not know", debianAccounts(), identity(2000, 2000), nil, false},
		{"group of the same number is not an administrator group", fedora, identity(1001, sudoGID), nil, false},
		{"wheel on a host that has none", debianAccounts(), identity(2000, wheelGID), nil, false},
		{"the user of a development daemon", debianAccounts(), identity(1001, 1001), &own, true},
		{"another user of a development daemon", debianAccounts(), identity(1003, 1003), &own, false},
		{"the same user, but the daemon is root", debianAccounts(), identity(1001, 1001), nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := administrators{accounts: tt.accounts, daemonUID: tt.daemonUID}.is(tt.who)
			if err != nil || got != tt.want {
				t.Fatalf("is() = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

// What the account database cannot say is not an administrator, and the policy
// says why in the refusal.
func TestLinuxAdministratorsFailClosedAndSayWhy(t *testing.T) {
	broken := accountDatabase{
		groupID:  func(name string) (uint32, bool, error) { return 0, false, errors.New("nsswitch unreachable") },
		groupIDs: func(uint32) ([]uint32, error) { return nil, errors.New("sssd is not answering") },
	}
	p := linuxPolicy(broken, seatsAbsent, nil)

	err := p.check(identity(1000, 1000), pb.DaemonService_DeleteProfile_FullMethodName)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code = %v (%v), want PermissionDenied", status.Code(err), err)
	}
	for _, want := range []string{"uid 1000", "look up group sudo: nsswitch unreachable", "read the groups of uid 1000: sssd is not answering"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	// Root and the user of a development daemon do not depend on the database.
	if err := p.check(identity(0, 0), pb.DaemonService_DeleteProfile_FullMethodName); err != nil {
		t.Errorf("root was refused: %v", err)
	}

	// A group that could not be looked up does not hide the one that could.
	partly := debianAccounts()
	partly.groupID = func(name string) (uint32, bool, error) {
		if name == "wheel" {
			return 0, false, errors.New("lookup failed")
		}
		return debianAccounts().groupID(name)
	}
	if got, err := (administrators{accounts: partly}).is(identity(1000, 1000)); !got || err != nil {
		t.Errorf("member of sudo with a failing wheel lookup: %v, %v", got, err)
	}
}

func TestLinuxPolicyDecisions(t *testing.T) {
	seats := writeSeats(t, map[string]string{"seat0": seatFile("ACTIVE=2", "ACTIVE_UID=1001")})
	own := uint32(1004)
	tests := []struct {
		name    string
		who     peercred.AuthInfo
		connect bool
		modify  bool
	}{
		{"root", identity(0, 0), true, true},
		{"administrator", identity(1000, 1000), true, true},
		{"administrator by the primary group of the account", identity(1002, sudoGID), true, true},
		{"user at the console", identity(1001, 1001), true, false},
		{"administrator who is not at the console", identity(1000, 1000), true, true},
		{"another user", identity(1003, 1003), false, false},
		{"the user who runs the development daemon", identity(1004, 1004), true, true},
		{"unknown identity", peercred.AuthInfo{}, false, false},
		{"unknown identity that claims root", peercred.AuthInfo{Info: peercred.Info{UID: 0, Groups: []uint32{sudoGID}}}, false, false},
	}
	p := linuxPolicy(debianAccounts(), seats, &own)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, method := range connectMethods {
				if allowed := p.check(tt.who, method) == nil; allowed != tt.connect {
					t.Errorf("%s allowed = %v, want %v", method, allowed, tt.connect)
				}
			}
			for _, method := range modifyMethods {
				if allowed := p.check(tt.who, method) == nil; allowed != tt.modify {
					t.Errorf("%s allowed = %v, want %v", method, allowed, tt.modify)
				}
			}
		})
	}
}

// The tests above take the accounts from a fake. This one reads the account
// database of the machine that runs the tests.
func TestSystemAccountsAgreeWithTheKernel(t *testing.T) {
	accounts := systemAccounts()
	groups, err := accounts.groupIDs(uint32(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) == 0 {
		t.Skipf("uid %d has no account here", os.Getuid())
	}
	gid := uint32(os.Getgid())
	found := false
	for _, g := range groups {
		found = found || g == gid
	}
	if !found {
		t.Errorf("groups of uid %d are %v, which lack its primary group %d", os.Getuid(), groups, gid)
	}

	g, err := user.LookupGroupId(strconv.Itoa(os.Getgid()))
	if err != nil {
		t.Skipf("group %d has no name here: %v", os.Getgid(), err)
	}
	if got, ok, err := accounts.groupID(g.Name); err != nil || !ok || got != gid {
		t.Errorf("groupID(%q) = %d, %v, %v; want %d", g.Name, got, ok, err, gid)
	}
	if _, ok, err := accounts.groupID("plaitway-no-such-group"); ok || err != nil {
		t.Errorf("a group that does not exist: ok %v, err %v; want neither", ok, err)
	}
}
