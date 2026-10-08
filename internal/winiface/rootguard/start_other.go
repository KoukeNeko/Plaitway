//go:build !windows

package rootguard

import "os/exec"

// detach has nothing to do off Windows: the root tests that need a guard are
// Windows ones.
func detach(*exec.Cmd) {}
