//go:build !windows

package main

// defaultEditor is what edit runs when neither $VISUAL nor $EDITOR is set.
const defaultEditor = "vi"

// splitEditorCommand reads the command as a shell does: quotes group and a
// backslash escapes.
func splitEditorCommand(command string) ([]string, error) {
	return splitWords(command), nil
}
