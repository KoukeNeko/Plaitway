package windows

import (
	"os"
	"testing"
)

// The unit tests of the DNS code exercise the policy refresh, which the shipped
// default leaves off (see defaultRefreshPolicyAfterChange).
func TestMain(m *testing.M) {
	refreshPolicyAfterChange = true
	os.Exit(m.Run())
}

func TestThePolicyRefreshIsOffByDefault(t *testing.T) {
	if defaultRefreshPolicyAfterChange {
		t.Error("the shipped default asks the DNS Client to reread its policy after every change; measured on Windows 11 it picks a registry rule up by itself")
	}
}
