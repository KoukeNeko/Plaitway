//go:build !unix && !windows

package main

import "os"

func stderrIsTerminal() bool { return true }

func redirectStderr(*os.File) error { return nil }
