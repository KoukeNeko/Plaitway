//go:build windows

// Command layoutcheck runs the check that `plaitwayd install` makes of its own
// executable (fsperm.CheckAdminOnlyPath) on a path and prints the verdict, so
// that the installer's tests ask the daemon's code and not a copy of its rules.
//
//	layoutcheck -path "C:\Program Files\Plaitway\plaitwayd.exe"
//
// Exit codes: 0 the path passes, 1 it is refused (the verdict says why), 2 wrong
// arguments. Nothing is changed.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/KoukeNeko/Plaitway/internal/fsperm"
)

const (
	exitRefused = 1
	exitUsage   = 2
)

// verdictReasons name what fsperm refuses, in the words the tests look for.
var verdictReasons = []struct {
	cause error
	name  string
}{
	{fsperm.ErrUntrustedOwner, "untrusted-owner"},
	{fsperm.ErrWritableByStandardUsers, "writable-by-standard-users"},
	{fsperm.ErrReparsePoint, "reparse-point"},
	{fsperm.ErrNotLocalFixedDisk, "not-local-fixed-disk"},
}

func main() {
	path := flag.String("path", "", "the file whose location is checked")
	flag.Parse()
	if *path == "" || flag.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "usage: layoutcheck -path <file>")
		os.Exit(exitUsage)
	}
	os.Exit(report(*path, fsperm.CheckAdminOnlyPath(*path)))
}

// report prints the verdict and returns the exit code for it.
func report(path string, err error) int {
	if err == nil {
		fmt.Printf("verdict=pass path=%s\n", path)
		return 0
	}
	fmt.Printf("verdict=refused reason=%s path=%s detail=%q\n", reasonOf(err), path, err.Error())
	return exitRefused
}

func reasonOf(err error) string {
	for _, known := range verdictReasons {
		if errors.Is(err, known.cause) {
			return known.name
		}
	}
	return "other"
}
