//go:build !windows

package main

import (
	"strings"
	"syscall"
)

// dialFailure picks the reason out of the message gRPC gives for a socket it
// could not connect to: connection error: desc = "transport: Error while
// dialing: dial unix /path: connect: no such file or directory".
func dialFailure(msg string) (reason string, ok bool) {
	const marker = ": connect: "
	i := strings.LastIndex(msg, marker)
	if i < 0 {
		return "", false
	}
	return strings.TrimSuffix(msg[i+len(marker):], `"`), true
}

// refusal has nothing to say: a socket is protected by its file mode, and the
// daemon checks the caller, not the other way round.
func refusal(string) (detail string, ok bool) { return "", false }

func classifyDialFailure(reason string) dialProblem {
	switch reason {
	case syscall.ENOENT.Error():
		return socketMissing
	case syscall.ECONNREFUSED.Error():
		return nobodyListens
	case syscall.EACCES.Error():
		return accessDenied
	}
	return otherProblem
}
