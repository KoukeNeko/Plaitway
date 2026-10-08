package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// $EDITOR is mostly set to a path with spaces and no quotes.
func TestEditorCommandNamingAFileWithSpacesNeedsNoQuotes(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "Program Files", "My Editor")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(dir, "ed.exe")
	if err := os.WriteFile(program, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	getenv := func(name string) string { return map[string]string{"EDITOR": program}[name] }

	got, err := editorCommand(getenv)
	if err != nil || !slices.Equal(got, []string{program}) {
		t.Errorf("editorCommand = %q, %v; want %q", got, err, program)
	}

	// With an argument it is no file any more, and the quotes are needed.
	getenv = func(name string) string { return map[string]string{"EDITOR": `"` + program + `" --wait`}[name] }
	got, err = editorCommand(getenv)
	if err != nil || !slices.Equal(got, []string{program, "--wait"}) {
		t.Errorf("editorCommand with quotes = %q, %v; want %q and --wait", got, err, program)
	}
}

func TestEditorCommandWithoutAnyVariableIsNotepad(t *testing.T) {
	t.Parallel()
	got, err := editorCommand(func(string) string { return "" })
	if err != nil || !slices.Equal(got, []string{"notepad"}) {
		t.Errorf("editorCommand = %q, %v", got, err)
	}
}
