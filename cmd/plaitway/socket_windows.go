package main

import (
	"fmt"
	"strings"
)

// pipeNamespace is where the system keeps named pipes.
const pipeNamespace = `\\.\pipe\`

// defaultSocket is the named pipe the daemon listens on (transport.DefaultPath).
const defaultSocket = pipeNamespace + "plaitway"

// checkSocketPath refuses what is not a local named pipe. Connecting to a file
// would open it, fail with "The parameter is incorrect", and say nothing about
// the path being wrong.
func checkSocketPath(path string) error {
	if len(path) > len(pipeNamespace) && strings.EqualFold(path[:len(pipeNamespace)], pipeNamespace) {
		return nil
	}
	return fmt.Errorf("%s is not a named pipe, use %s<name>", path, pipeNamespace)
}
