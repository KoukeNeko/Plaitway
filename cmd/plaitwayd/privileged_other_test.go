//go:build !windows

package main

import (
	"os"
	"testing"
)

func TestIsPrivilegedMeansRoot(t *testing.T) {
	if got, want := isPrivileged(), os.Geteuid() == 0; got != want {
		t.Fatalf("isPrivileged() = %v with euid %d", got, os.Geteuid())
	}
}
