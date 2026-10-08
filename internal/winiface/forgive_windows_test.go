//go:build windows

package winiface

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

func TestForgiveAbsentAddress(t *testing.T) {
	lookupFailed := errors.New("lookup failed")
	there := func() (bool, error) { return true, nil }
	absent := func() (bool, error) { return false, nil }
	broken := func() (bool, error) { return false, lookupFailed }
	never := func() (bool, error) { t.Fatal("the address was looked up for an error that needs no lookup"); return false, nil }
	other := errors.New("access is denied")

	tests := []struct {
		name  string
		err   error
		check func() (bool, error)
		want  error
	}{
		{"the delete worked", nil, never, nil},
		{"not found: gone already", windows.ERROR_NOT_FOUND, never, nil},
		{"a wrong parameter for an address that is not there is the same as gone", windows.ERROR_INVALID_PARAMETER, absent, nil},
		{"a wrong parameter for an address that is there stays an error", windows.ERROR_INVALID_PARAMETER, there, windows.ERROR_INVALID_PARAMETER},
		{"a wrong parameter that cannot be checked stays an error", windows.ERROR_INVALID_PARAMETER, broken, windows.ERROR_INVALID_PARAMETER},
		{"any other error stays an error and needs no lookup", other, never, other},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := forgiveAbsentAddress(tt.err, tt.check)
			if !errors.Is(got, tt.want) && !(tt.want == nil && got == nil) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
			if tt.want == nil && got != nil {
				t.Errorf("got %v, want nil", got)
			}
		})
	}
}
