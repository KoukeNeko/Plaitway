//go:build darwin || linux

package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func waitUntil(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type answer struct {
	text string
	err  error
}

func ask(ctx context.Context, prompt func(context.Context, string, bool) (string, error), label string, secret bool) <-chan answer {
	done := make(chan answer, 1)
	go func() {
		text, err := prompt(ctx, label, secret)
		done <- answer{text, err}
	}()
	return done
}

func TestIsTerminal(t *testing.T) {
	t.Parallel()
	_, slave, _ := openPTY(t)
	if !isTerminal(slave) {
		t.Error("a pseudo terminal is not a terminal")
	}
	file, err := os.CreateTemp(t.TempDir(), "plain")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if isTerminal(file) {
		t.Error("a file is a terminal")
	}
}

func TestPasswordPromptDoesNotEchoAndRestoresTheTerminal(t *testing.T) {
	t.Parallel()
	master, slave, echoed := openPTY(t)
	var question syncBuffer
	prompt := terminalPrompt(slave, &question)

	if !echoes(t, slave) {
		t.Fatal("a new terminal does not echo")
	}
	done := ask(context.Background(), prompt, "Password for home: ", true)
	waitUntil(t, "the echo to be off", func() bool { return !echoes(t, slave) })
	if _, err := master.WriteString("hunter2\n"); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || got.text != "hunter2" {
		t.Errorf("answer %q, %v", got.text, got.err)
	}
	if !echoes(t, slave) {
		t.Error("the terminal still does not echo after the prompt")
	}
	if q := question.String(); q != "Password for home: \n" {
		t.Errorf("the prompt wrote %q, want the question and a line break", q)
	}

	// The same terminal echoes a user name, which shows that the check above
	// would have noticed an echoed password.
	done = ask(context.Background(), prompt, "Username for home: ", false)
	waitUntil(t, "the question", func() bool { return strings.Contains(question.String(), "Username") })
	if _, err := master.WriteString("alice\n"); err != nil {
		t.Fatal(err)
	}
	if got := <-done; got.err != nil || got.text != "alice" {
		t.Errorf("answer %q, %v", got.text, got.err)
	}
	waitUntil(t, "the echo of the user name", func() bool { return strings.Contains(echoed.String(), "alice") })
	if strings.Contains(echoed.String(), "hunter2") {
		t.Errorf("the password was echoed: %q", echoed.String())
	}
}

func TestInterruptedPasswordPromptRestoresTheTerminal(t *testing.T) {
	t.Parallel()
	_, slave, _ := openPTY(t)
	var question syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	done := ask(ctx, terminalPrompt(slave, &question), "Password: ", true)
	waitUntil(t, "the echo to be off", func() bool { return !echoes(t, slave) })

	cancel()
	got := <-done
	if got.err != errInterrupted || got.text != "" {
		t.Errorf("answer %q, %v", got.text, got.err)
	}
	if !echoes(t, slave) {
		t.Error("the terminal does not echo after an interrupted prompt")
	}
}

// Color is for a terminal, and NO_COLOR and TERM=dumb turn it off there.
func TestBinaryColorsOnlyOnATerminal(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	d.importText("home", wgProfile)
	d.mustRun("", "connect", "home")

	onATerminal := func(env ...string) string {
		_, slave, output := openPTY(t)
		cmd := exec.Command(clientBinary, "status")
		cmd.Env = append([]string{"PLAITWAY_SOCKET=" + d.socket}, env...)
		cmd.Stdout = slave
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
		// The terminal turns line feeds into CR LF; what matters is that the table arrived.
		waitUntil(t, "the table", func() bool { return strings.Contains(output.String(), "connected") })
		return output.String()
	}

	if got := onATerminal("TERM=xterm-256color"); !strings.Contains(got, ansiGreen+"connected"+ansiReset) {
		t.Errorf("a terminal got no color: %q", got)
	}
	for _, env := range [][]string{{"TERM=xterm-256color", "NO_COLOR=1"}, {"TERM=dumb"}} {
		if got := onATerminal(env...); strings.Contains(got, "\x1b") {
			t.Errorf("%v: color although it is turned off: %q", env, got)
		}
	}
}
