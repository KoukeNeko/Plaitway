//go:build !linux

package main

// notRunningHint: only the Linux client says which service to look at.
func notRunningHint(string) string { return "" }
