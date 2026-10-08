//go:build unix

package main

import "testing"

func TestRealConsoleOwnerIsReadable(t *testing.T) {
	if _, err := fileOwner(consoleDevice); err != nil {
		t.Skipf("no console device here: %v", err)
	}
	if _, err := newConsoleUser().uid(); err != nil {
		t.Fatal(err)
	}
}
