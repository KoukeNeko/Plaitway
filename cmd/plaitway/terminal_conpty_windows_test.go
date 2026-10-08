package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The prompt is tested in a program of its own, started inside a pseudo
// console: it has a console that is not the one of whoever runs the tests, and
// what the test types goes to the pseudo console's input pipe, never to a real
// keyboard buffer.

// promptHelperFlag, as the first argument, makes the test binary ask one
// question at its console; see helperPrograms.
const promptHelperFlag = "-plaitway-prompt-helper"

const (
	secretQuestion = "secret"
	plainQuestion  = "plain"

	promptLabel       = "Question: "
	pseudoConsoleCols = 120
	pseudoConsoleRows = 30
	pollInterval      = 10 * time.Millisecond
)

func init() { helperPrograms[promptHelperFlag] = runPromptHelper }

// runPromptHelper asks the question, with the echo off for a secret one, and
// records the answer and the state of the console afterwards in a file. The
// marker file tells the test that the console is ready to be typed at: typing
// earlier would be discarded by disableEcho, which flushes the input.
func runPromptHelper(args []string) int {
	if len(args) != 3 {
		fmt.Fprintln(os.Stderr, "usage:", promptHelperFlag, "secret|plain markerFile resultFile")
		return 2
	}
	kind, markerFile, resultFile := args[0], args[1], args[2]
	secret := kind == secretQuestion
	// Not os.Stdin and os.Stdout: CreateProcess hands a child the standard
	// handles of its parent unless it is told otherwise, and the console is
	// where the test types.
	input, output, err := openOwnConsole()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	go createMarkerWhenReady(markerFile, input, secret)

	answer, err := terminalPrompt(input, output)(context.Background(), promptLabel, secret)
	var mode uint32
	modeErr := windows.GetConsoleMode(windows.Handle(input.Fd()), &mode)
	record := fmt.Sprintf("answer=%s\nerror=%v\nmode-error=%v\necho-after=%t\n", answer, err, modeErr, mode&windows.ENABLE_ECHO_INPUT != 0)
	if writeErr := os.WriteFile(resultFile, []byte(record), 0o600); writeErr != nil {
		fmt.Fprintln(os.Stderr, writeErr)
		return 1
	}
	return 0
}

func openOwnConsole() (input, output *os.File, err error) {
	for _, name := range []string{"CONIN$", "CONOUT$"} {
		path, err := windows.UTF16PtrFromString(name)
		if err != nil {
			return nil, nil, err
		}
		handle, err := windows.CreateFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
		if err != nil {
			return nil, nil, fmt.Errorf("open %s: %w", name, err)
		}
		if name == "CONIN$" {
			input = os.NewFile(uintptr(handle), name)
		} else {
			output = os.NewFile(uintptr(handle), name)
		}
	}
	return input, output, nil
}

// createMarkerWhenReady waits for the echo to be in the state the question
// needs, which for a secret one is the state disableEcho leaves.
func createMarkerWhenReady(markerFile string, input *os.File, secret bool) {
	for {
		var mode uint32
		if err := windows.GetConsoleMode(windows.Handle(input.Fd()), &mode); err == nil && (mode&windows.ENABLE_ECHO_INPUT == 0) == secret {
			break
		}
		time.Sleep(pollInterval)
	}
	if err := os.WriteFile(markerFile, nil, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

// pseudoConsoleSession is a program running in a pseudo console.
type pseudoConsoleSession struct {
	typed   *os.File
	output  *syncBuffer
	process windows.Handle
	console windows.Handle
	drained chan struct{}
}

// startInPseudoConsole runs this test binary with args in a pseudo console of
// its own.
func startInPseudoConsole(t *testing.T, args ...string) *pseudoConsoleSession {
	t.Helper()
	var inputRead, inputWrite, outputRead, outputWrite windows.Handle
	if err := windows.CreatePipe(&inputRead, &inputWrite, nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := windows.CreatePipe(&outputRead, &outputWrite, nil, 0); err != nil {
		t.Fatal(err)
	}
	var console windows.Handle
	size := windows.Coord{X: pseudoConsoleCols, Y: pseudoConsoleRows}
	if err := windows.CreatePseudoConsole(size, inputRead, outputWrite, 0, &console); err != nil {
		t.Skipf("no pseudo console on this Windows: %v", err)
	}
	// The pseudo console holds its own references to the ends it uses.
	windows.CloseHandle(inputRead)
	windows.CloseHandle(outputWrite)

	session := &pseudoConsoleSession{
		typed:   os.NewFile(uintptr(inputWrite), "pseudo console input"),
		output:  &syncBuffer{},
		console: console,
		drained: make(chan struct{}),
	}
	t.Cleanup(session.close)
	outputPipe := os.NewFile(uintptr(outputRead), "pseudo console output")
	go func() {
		defer close(session.drained)
		defer outputPipe.Close()
		io.Copy(session.output, outputPipe)
	}()

	session.process = createProcessIn(t, console, append([]string{os.Args[0]}, args...))
	return session
}

func createProcessIn(t *testing.T, console windows.Handle, commandLine []string) windows.Handle {
	t.Helper()
	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		t.Fatal(err)
	}
	defer attributes.Delete()
	// The attribute value is the console handle itself, not a pointer to it.
	consoleValue := *(*unsafe.Pointer)(unsafe.Pointer(&console))
	if err := attributes.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, consoleValue, unsafe.Sizeof(console)); err != nil {
		t.Fatal(err)
	}
	startup := &windows.StartupInfoEx{
		StartupInfo:             windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{}))},
		ProcThreadAttributeList: attributes.List(),
	}
	line, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(commandLine))
	if err != nil {
		t.Fatal(err)
	}
	var process windows.ProcessInformation
	if err := windows.CreateProcess(nil, line, nil, nil, false, windows.EXTENDED_STARTUPINFO_PRESENT, nil, nil, &startup.StartupInfo, &process); err != nil {
		t.Fatal(err)
	}
	windows.CloseHandle(process.Thread)
	return process.Process
}

// waitForExit fails the test when the program has not ended in time.
func (s *pseudoConsoleSession) waitForExit(t *testing.T) {
	t.Helper()
	event, err := windows.WaitForSingleObject(s.process, uint32(waitTimeout.Milliseconds()))
	if err != nil || event != windows.WAIT_OBJECT_0 {
		t.Fatalf("the program in the pseudo console did not end (event %#x, error %v)", event, err)
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(s.process, &exitCode); err != nil || exitCode != 0 {
		t.Fatalf("the program in the pseudo console exited with %d (error %v); it wrote %q", exitCode, err, s.output.String())
	}
}

// finish closes the pseudo console, which ends its output, and returns all that
// the program wrote to it.
func (s *pseudoConsoleSession) finish(t *testing.T) string {
	t.Helper()
	s.close()
	select {
	case <-s.drained:
	case <-time.After(waitTimeout):
		t.Fatal("the output of the pseudo console did not end")
	}
	return s.output.String()
}

// close is safe to call twice: finish calls it, and so does the cleanup of a
// test that failed before.
func (s *pseudoConsoleSession) close() {
	if s.console != 0 {
		windows.TerminateProcess(s.process, 1)
		windows.ClosePseudoConsole(s.console)
		s.console = 0
		s.typed.Close()
		windows.CloseHandle(s.process)
	}
}

func awaitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for {
		_, err := os.Stat(path)
		if err == nil {
			return
		}
		if !errors.Is(err, os.ErrNotExist) || time.Now().After(deadline) {
			t.Fatalf("waiting for %s: %v", path, err)
		}
		time.Sleep(pollInterval)
	}
}

// askAtAPseudoConsole has the helper ask its question, types the answer, and
// returns what the helper recorded and everything the console showed.
func askAtAPseudoConsole(t *testing.T, kind, typed string) (record, shown string) {
	t.Helper()
	dir := t.TempDir()
	markerFile, resultFile := filepath.Join(dir, "ready"), filepath.Join(dir, "result")
	session := startInPseudoConsole(t, promptHelperFlag, kind, markerFile, resultFile)

	awaitFile(t, markerFile)
	if _, err := session.typed.WriteString(typed + "\r"); err != nil {
		t.Fatal(err)
	}
	session.waitForExit(t)
	recorded, err := os.ReadFile(resultFile)
	if err != nil {
		t.Fatal(err)
	}
	return string(recorded), session.finish(t)
}

func TestPasswordPromptAtAConsoleReadsTheAnswerWithoutEchoingIt(t *testing.T) {
	const password = "hunter2"
	record, shown := askAtAPseudoConsole(t, secretQuestion, password)

	if want := "answer=" + password + "\nerror=<nil>\nmode-error=<nil>\necho-after=true\n"; record != want {
		t.Errorf("recorded %q, want %q", record, want)
	}
	// The console draws the trailing space as a cursor movement.
	if !strings.Contains(shown, strings.TrimSpace(promptLabel)) {
		t.Errorf("the console never showed the question: %q", shown)
	}
	if strings.Contains(shown, password) {
		t.Errorf("the password was echoed: %q", shown)
	}
}

// The same console echoes an answer that is not secret, which shows that the
// check above would have noticed an echoed password.
func TestPlainPromptAtAConsoleEchoesTheAnswer(t *testing.T) {
	const answer = "alice"
	record, shown := askAtAPseudoConsole(t, plainQuestion, answer)

	if want := "answer=" + answer + "\nerror=<nil>\nmode-error=<nil>\necho-after=true\n"; record != want {
		t.Errorf("recorded %q, want %q", record, want)
	}
	if !strings.Contains(shown, answer) {
		t.Errorf("the console did not echo the answer: %q", shown)
	}
}
