package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
)

// seatFile is a seat file of systemd-logind as it writes it (the first line is
// its own), followed by lines.
func seatFile(lines ...string) string {
	return "# This is private data. Do not parse.\nIS_SEAT0=1\nCAN_MULTI_SESSION=1\n" + strings.Join(lines, "\n") + "\nSESSIONS=1\n"
}

// writeSeats makes a directory like /run/systemd/seats.
func writeSeats(t *testing.T, seats map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range seats {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestSeatUsersAreTheActiveUsersOfEverySeat(t *testing.T) {
	dir := writeSeats(t, map[string]string{
		"seat0": seatFile("ACTIVE=2", "ACTIVE_UID=1000", "UIDS=1000"),
		"seat1": seatFile("ACTIVE=c4", "ACTIVE_UID=1001"),
		// Nobody is logged in at the greeter of this seat.
		"seat2": seatFile("CAN_GRAPHICAL=1"),
	})
	got, err := seatUsers(dir)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	if want := []uint32{1000, 1001}; !slices.Equal(got, want) {
		t.Fatalf("seatUsers = %v, want %v", got, want)
	}
}

func TestSeatUsersWithNobodyLoggedInIsEmptyNotAnError(t *testing.T) {
	got, err := seatUsers(writeSeats(t, map[string]string{"seat0": seatFile("CAN_GRAPHICAL=1")}))
	if err != nil || len(got) != 0 {
		t.Fatalf("seatUsers = %v, %v; want no user and no error", got, err)
	}
}

// The file of this machine, as logind wrote it.
func TestSeatUsersReadTheFormatOfLogind(t *testing.T) {
	const seat0 = "# This is private data. Do not parse.\nIS_SEAT0=1\nCAN_MULTI_SESSION=1\nCAN_TTY=1\nCAN_GRAPHICAL=1\nACTIVE=1\nACTIVE_UID=1000\nSESSIONS=1\nUIDS=1000\n"
	got, err := seatUsers(writeSeats(t, map[string]string{"seat0": seat0}))
	if err != nil || !slices.Equal(got, []uint32{1000}) {
		t.Fatalf("seatUsers = %v, %v; want [1000]", got, err)
	}
}

func TestSeatUsersFailWithoutLogindState(t *testing.T) {
	_, err := seatUsers(seatsAbsent)
	if err == nil || !strings.Contains(err.Error(), "systemd-logind has no seat state") || !strings.Contains(err.Error(), seatsAbsent) {
		t.Fatalf("err = %v, want it to say that logind has no seat state and where it looked", err)
	}

	// A seat file that cannot be understood is not skipped: who is at the
	// console is then not known.
	dir := writeSeats(t, map[string]string{
		"seat0": seatFile("ACTIVE=1", "ACTIVE_UID=1000"),
		"seat1": seatFile("ACTIVE=2", "ACTIVE_UID=bob"),
	})
	if _, err := seatUsers(dir); err == nil || !strings.Contains(err.Error(), "seat1") || !strings.Contains(err.Error(), `"bob"`) {
		t.Fatalf("err = %v, want it to name the seat and the value", err)
	}
}

func TestLinuxPolicyFailsClosedWithoutLogind(t *testing.T) {
	p := linuxPolicy(debianAccounts(), seatsAbsent, nil)

	// Only administrators are authorized.
	err := p.check(identity(1001, 1001), pb.DaemonService_ListProfiles_FullMethodName)
	if status.Code(err) != codes.PermissionDenied || !strings.Contains(err.Error(), "systemd-logind has no seat state") {
		t.Fatalf("a user without logind: %v, want PermissionDenied that names the missing seat state", err)
	}
	for _, administrator := range []struct {
		name string
		uid  uint32
		gid  uint32
	}{{"root", 0, 0}, {"member of sudo", 1000, 1000}} {
		if err := p.check(identity(administrator.uid, administrator.gid), pb.DaemonService_DeleteProfile_FullMethodName); err != nil {
			t.Errorf("%s was refused without logind: %v", administrator.name, err)
		}
	}
}

func TestLinuxPolicyFollowsLoginsWithinASecond(t *testing.T) {
	dir := writeSeats(t, map[string]string{"seat0": seatFile("ACTIVE=1", "ACTIVE_UID=1001")})
	p := linuxPolicy(debianAccounts(), dir, nil)
	method := pb.DaemonService_ListProfiles_FullMethodName
	if err := p.check(identity(1001, 1001), method); err != nil {
		t.Fatalf("the user at the console: %v", err)
	}
	// 1003 logs in after the answer was read: it is read again after a second.
	if err := os.WriteFile(filepath.Join(dir, "seat0"), []byte(seatFile("ACTIVE=3", "ACTIVE_UID=1003")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.check(identity(1003, 1003), method); err == nil {
		t.Error("the new console user was accepted before the cached answer expired")
	}
	time.Sleep(consoleRefresh + 50*time.Millisecond)
	if err := p.check(identity(1003, 1003), method); err != nil {
		t.Errorf("the new console user was refused after the answer expired: %v", err)
	}
	if err := p.check(identity(1001, 1001), method); err == nil {
		t.Error("the user who left the console is still accepted")
	}
}

func TestRefreshedReadsAtMostOncePerInterval(t *testing.T) {
	now := time.Unix(1000, 0)
	reads := 0
	read := refreshed(func() ([]uint32, error) { reads++; return []uint32{uint32(reads)}, nil }, func() time.Time { return now })

	for range 5 {
		if got, _ := read(); got[0] != 1 {
			t.Fatalf("got %v, want the first answer", got)
		}
	}
	now = now.Add(consoleRefresh - time.Millisecond)
	if got, _ := read(); got[0] != 1 || reads != 1 {
		t.Fatalf("got %v after %d reads, want the cached answer", got, reads)
	}
	now = now.Add(time.Millisecond)
	if got, _ := read(); got[0] != 2 || reads != 2 {
		t.Fatalf("got %v after %d reads, want a second read", got, reads)
	}
}

func TestRefreshedKeepsFailuresToo(t *testing.T) {
	now := time.Unix(1000, 0)
	reads := 0
	read := refreshed(func() (int, error) { reads++; return 0, os.ErrNotExist }, func() time.Time { return now })
	for range 3 {
		if _, err := read(); err == nil {
			t.Fatal("error lost")
		}
	}
	if reads != 1 {
		t.Fatalf("%d reads, want 1", reads)
	}
}

// The seats of the machine that runs the tests, if systemd-logind is there.
func TestRealSeatsAreReadable(t *testing.T) {
	if _, err := os.Stat(logindSeatsDir); err != nil {
		t.Skipf("no systemd-logind here: %v", err)
	}
	uids, err := seatUsers(logindSeatsDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("users at the console of this machine: %v", uids)
}
