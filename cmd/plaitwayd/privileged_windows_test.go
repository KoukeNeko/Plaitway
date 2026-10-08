package main

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// securityMandatoryHighRID is SECURITY_MANDATORY_HIGH_RID, which x/sys/windows
// does not define. An elevated administrator runs at this integrity level and
// LocalSystem at the one above it, so "at or above" is the elevated test.
const securityMandatoryHighRID = 0x3000

// integrityRID reads the integrity level of the token, the last sub-authority
// of its mandatory label SID. The elevation flag that isPrivileged reads is a
// different token field, so this is an independent oracle.
func integrityRID(t *testing.T, token windows.Token) uint32 {
	t.Helper()
	var size uint32
	if err := windows.GetTokenInformation(token, windows.TokenIntegrityLevel, nil, 0, &size); err != windows.ERROR_INSUFFICIENT_BUFFER {
		t.Fatalf("size of the integrity label: %v", err)
	}
	buffer := make([]byte, size)
	if err := windows.GetTokenInformation(token, windows.TokenIntegrityLevel, &buffer[0], size, &size); err != nil {
		t.Fatalf("integrity label: %v", err)
	}
	label := (*windows.Tokenmandatorylabel)(unsafe.Pointer(&buffer[0]))
	sid := label.Label.Sid
	return sid.SubAuthority(uint32(sid.SubAuthorityCount()) - 1)
}

func TestIsPrivilegedFollowsTheIntegrityLevelOfTheToken(t *testing.T) {
	rid := integrityRID(t, windows.GetCurrentProcessToken())
	if got, want := isPrivileged(), rid >= securityMandatoryHighRID; got != want {
		t.Fatalf("isPrivileged() = %v, but the token's integrity level is %#x", got, rid)
	}
}
