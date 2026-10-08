package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// defaultEditor is what edit runs when neither $VISUAL nor $EDITOR is set.
const defaultEditor = "notepad"

// splitEditorCommand reads the command as Windows reads a command line, where
// a backslash is part of a path and only double quotes group. A command that
// is the path of a file as it stands is not split: the variable is mostly set
// to a path with spaces and no quotes.
func splitEditorCommand(command string) ([]string, error) {
	if info, err := os.Stat(command); err == nil && info.Mode().IsRegular() {
		return []string{command}, nil
	}
	return windows.DecomposeCommandLine(command)
}
