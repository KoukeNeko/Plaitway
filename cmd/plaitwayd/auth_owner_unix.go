//go:build unix

package main

import (
	"fmt"
	"os"
	"syscall"
)

// fileOwner returns the uid that owns path.
func fileOwner(path string) (uint32, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("no owner information for %s", path)
	}
	return st.Uid, nil
}
