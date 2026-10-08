//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/fsperm"
)

func TestReasonOfNamesWhatFspermRefuses(t *testing.T) {
	tests := []struct {
		cause error
		want  string
	}{
		{fmt.Errorf("a: %w", fsperm.ErrUntrustedOwner), "untrusted-owner"},
		{fmt.Errorf("b: %w", fsperm.ErrWritableByStandardUsers), "writable-by-standard-users"},
		{fmt.Errorf("c: %w", fsperm.ErrReparsePoint), "reparse-point"},
		{fmt.Errorf("d: %w", fsperm.ErrNotLocalFixedDisk), "not-local-fixed-disk"},
		{os.ErrNotExist, "other"},
	}
	for _, tt := range tests {
		if got := reasonOf(tt.cause); got != tt.want {
			t.Errorf("reasonOf(%v) = %q, want %q", tt.cause, got, tt.want)
		}
	}
}

func TestReportExitCodes(t *testing.T) {
	if code := report(`C:\x`, nil); code != 0 {
		t.Errorf("a passing path exits %d, want 0", code)
	}
	if code := report(`C:\x`, fsperm.ErrUntrustedOwner); code != exitRefused {
		t.Errorf("a refused path exits %d, want %d", code, exitRefused)
	}
}

// A file in the test's own temporary folder is owned by the user running the
// test, which is exactly what the check must refuse: it is the verdict on a
// layout that a standard user could change.
func TestAFileOfTheCurrentUserIsRefused(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "plaitwayd.exe")
	if err := os.WriteFile(executable, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := fsperm.CheckAdminOnlyPath(executable)

	if reason := reasonOf(err); err == nil || reason == "other" {
		t.Fatalf("CheckAdminOnlyPath(%s) = %v (reason %s), want a refusal for owner or access list", executable, err, reason)
	}
}
