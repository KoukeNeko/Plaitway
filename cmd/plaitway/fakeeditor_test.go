package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The editors of the edit tests are this test binary, started with
// fakeEditorFlag: a shell script does not run everywhere, and the editor has
// to be a program that $EDITOR can name on every OS.

// fakeEditorFlag, as the first argument, makes the binary an editor; see runTests.
const fakeEditorFlag = "-plaitway-fake-editor"

const (
	// queueEditor logs what it is given and replaces the file with the next text in a queue.
	queueEditor = "queue"
	// failingEditor changes the file and fails.
	failingEditor = "fail"
	// hangingEditor says it has started, then runs until it is killed.
	hangingEditor = "hang"

	failingEditorExitCode = 3
	// hangingEditorLifetime is long enough for any test to stop it first.
	hangingEditorLifetime = time.Minute
)

// runFakeEditor is the editor that args name: its kind, its own arguments, and
// the file to edit, which edit appends.
func runFakeEditor(args []string) int {
	if err := runEditorKind(args); err != nil {
		var exit exitCodeError
		if errors.As(err, &exit) {
			return int(exit)
		}
		fmt.Fprintln(os.Stderr, "fake editor:", err)
		return 1
	}
	return 0
}

// exitCodeError is an editor that ends with a status on purpose.
type exitCodeError int

func (e exitCodeError) Error() string { return "exit status " + strconv.Itoa(int(e)) }

func runEditorKind(args []string) error {
	if len(args) == 0 {
		return errors.New("no kind of editor given")
	}
	kind, args := args[0], args[1:]
	switch {
	case kind == queueEditor && len(args) == 3:
		return editFromQueue(args[0], args[1], args[2])
	case kind == failingEditor && len(args) == 1:
		return failToEdit(args[0])
	case kind == hangingEditor && len(args) == 2:
		return hangWhileEditing(args[0], args[1])
	}
	return fmt.Errorf("cannot run %s with %d arguments", kind, len(args))
}

// editFromQueue logs the text it was given, the file's path and who can reach the file and its
// directory, and then replaces the file with the next text in the queue, if
// there is one: the first run takes the first text.
func editFromQueue(queue, logDir, file string) error {
	runs, err := editorRuns(logDir)
	if err != nil {
		return err
	}
	run := runs + 1
	seen, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	access, err := describeAccess(file, filepath.Dir(file))
	if err != nil {
		return err
	}
	for name, content := range map[string]string{
		"count":                      strconv.Itoa(run) + "\n",
		"path":                       file + "\n",
		"seen." + strconv.Itoa(run):  string(seen),
		"modes." + strconv.Itoa(run): access,
	} {
		if err := os.WriteFile(filepath.Join(logDir, name), []byte(content), 0o600); err != nil {
			return err
		}
	}
	next, err := os.ReadFile(filepath.Join(queue, strconv.Itoa(run)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return os.WriteFile(file, next, 0o600)
}

// editorRuns is how often the editor has run before, from its log.
func editorRuns(logDir string) (int, error) {
	count, err := os.ReadFile(filepath.Join(logDir, "count"))
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(count)))
}

func failToEdit(file string) error {
	if err := os.WriteFile(file, []byte("changed\n"), 0o600); err != nil {
		return err
	}
	return exitCodeError(failingEditorExitCode)
}

// hangWhileEditing writes "file pid" to started once the test can look at the
// file, and then waits. An interrupt does not end it: the console sends one to
// the editor along with the command, and the test is about the command ending
// the editor.
func hangWhileEditing(started, file string) error {
	signal.Notify(make(chan os.Signal, 1), os.Interrupt)
	pending := started + ".tmp"
	if err := os.WriteFile(pending, []byte(file+" "+strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return err
	}
	if err := os.Rename(pending, started); err != nil {
		return err
	}
	time.Sleep(hangingEditorLifetime)
	return errors.New("nobody stopped the editor")
}

// fakeEditorProgram is the program that fakeEditorCommand names, as an editor
// error says it.
var fakeEditorProgram = currentExecutable()

func currentExecutable() string {
	executable, err := os.Executable()
	if err != nil {
		panic("the test binary does not know its own path: " + err.Error())
	}
	return executable
}

// fakeEditorCommand is $EDITOR for an editor of a kind: the test binary with
// the arguments, quoted so that a path with spaces stays one word.
func fakeEditorCommand(kind string, args ...string) string {
	words := append([]string{fakeEditorProgram, fakeEditorFlag, kind}, args...)
	for i, word := range words {
		words[i] = `"` + word + `"`
	}
	return strings.Join(words, " ")
}
