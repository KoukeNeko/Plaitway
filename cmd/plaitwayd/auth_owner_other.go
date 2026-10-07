//go:build !unix

package main

import "errors"

// There is no console device on Windows; peer identity is not implemented
// there either, so every call is refused before this matters.
func fileOwner(string) (uint32, error) {
	return 0, errors.New("the console user is not available on this OS")
}
