// Command plaitway is the command line client of the Plaitway daemon. It does
// what the menu bar app does, from a terminal or a script.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "0.0.0-dev"

// Exit codes; 0 is success.
const (
	exitFailure = 1
	exitUsage   = 2
)

// usageError is a command line that cannot be run, as opposed to a command
// that failed.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usageErrorf(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

// app is the process around the commands, so that tests can run them in
// process with their own streams.
type app struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	getenv         func(string) string
	// color is set when stdout is a terminal.
	color bool
	// prompt asks the person at the terminal; nil when stdin is not one.
	prompt func(ctx context.Context, label string, secret bool) (string, error)
	// socket is the -socket flag, empty when not given.
	socket string
}

func main() {
	a := &app{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr, getenv: os.Getenv}
	a.color = isTerminal(os.Stdout) && a.getenv("NO_COLOR") == "" && a.getenv("TERM") != "dumb"
	if isTerminal(os.Stdin) {
		a.prompt = terminalPrompt(os.Stdin, os.Stderr)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := a.run(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}

// run executes one command line and returns the exit code.
func (a *app) run(ctx context.Context, args []string) int {
	err := a.dispatch(ctx, args)
	var usage *usageError
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return 0
	case errors.As(err, &usage):
		fmt.Fprintf(a.stderr, "plaitway: %s\nRun \"plaitway help\" for usage.\n", clean(usage.msg))
		return exitUsage
	default:
		fmt.Fprintf(a.stderr, "plaitway: %s\n", clean(err.Error()))
		return exitFailure
	}
}

type command struct {
	name string
	// args is the positional part of the usage line.
	args    string
	summary string
	run     func(ctx context.Context, args []string) error
}

func (a *app) commands() []command {
	return []command{
		{"status", "[profile]", "show profiles with state, interface, addresses, uptime and traffic", a.status},
		{"list", "", "list profiles with their ids", a.list},
		{"connect", "profile", "connect a profile and wait until it is up", a.connect},
		{"disconnect", "profile", "disconnect a profile", a.disconnect},
		{"import", "file", "add a profile from an .ovpn or wg-quick .conf file", a.importProfile},
		{"show", "profile", "print the text of a profile, with its secrets hidden unless -secrets is given", a.show},
		{"edit", "profile", "change the text of a profile in $VISUAL or $EDITOR", a.edit},
		{"set", "profile", "change the name or the settings of a profile", a.set},
		{"remove", "profile", "disconnect and delete a profile", a.remove},
		{"logs", "[profile]", "show the log of a profile, or of the daemon without one", a.logs},
		{"diagnostics", "", "show the network, the routes the daemon owns and the journal", a.diagnostics},
		{"resync", "", "rebuild the routes and DNS entries the daemon owns", a.resync},
		{"watch", "", "print profile changes as they happen", a.watch},
		{"version", "", "print the version", a.printVersion},
	}
}

func (a *app) dispatch(ctx context.Context, args []string) error {
	fs := a.flagSet("plaitway")
	showVersion := fs.Bool("version", false, "print the version")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			a.usage(a.stdout)
			return nil
		}
		return usageErrorf("%v", err)
	}
	if *showVersion {
		return a.printVersion(ctx, nil)
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return usageErrorf("missing command")
	}
	if rest[0] == "help" {
		a.usage(a.stdout)
		return nil
	}
	for _, c := range a.commands() {
		if c.name == rest[0] {
			return c.run(ctx, rest[1:])
		}
	}
	return usageErrorf("unknown command %q", rest[0])
}

func (a *app) usage(w io.Writer) {
	fmt.Fprint(w, "Usage: plaitway [-socket path] <command> [flags] [arguments]\n\nCommands:\n")
	cmds := a.commands()
	width := 0
	for _, c := range cmds {
		width = max(width, len(strings.TrimSpace(c.name+" "+c.args)))
	}
	for _, c := range cmds {
		fmt.Fprintf(w, "  %-*s  %s\n", width, strings.TrimSpace(c.name+" "+c.args), c.summary)
	}
	fmt.Fprintf(w, "\nA profile is a name (case-insensitive) or an id; \"plaitway list\" shows both.\n"+
		"The daemon socket is -socket, else $PLAITWAY_SOCKET, else %s.\n"+
		"Run \"plaitway <command> -h\" for the flags of a command.\n", defaultSocket)
}

// flagSet returns the flags every command has. Flags are accepted before and
// after the arguments.
func (a *app) flagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard) // errors and help are printed by parse
	fs.StringVar(&a.socket, "socket", a.socket, "daemon socket (default $PLAITWAY_SOCKET, else "+defaultSocket+")")
	return fs
}

// parse reads the flags of a command from args wherever they stand and returns
// the arguments between minArgs and maxArgs that are left.
func (a *app) parse(fs *flag.FlagSet, args []string, minArgs, maxArgs int) ([]string, error) {
	var rest []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				a.commandHelp(fs)
				return nil, err
			}
			return nil, usageErrorf("%s: %v", fs.Name(), err)
		}
		left := fs.Args()
		// Parse stops at the first argument and swallows a "--": what follows
		// "--" is arguments only, even when it looks like a flag.
		if n := len(args) - len(left); n > 0 && args[n-1] == "--" {
			rest = append(rest, left...)
			break
		}
		if len(left) == 0 {
			break
		}
		rest = append(rest, left[0])
		args = left[1:]
	}
	if len(rest) < minArgs || len(rest) > maxArgs {
		return nil, usageErrorf("usage: plaitway %s", a.usageLine(fs.Name()))
	}
	return rest, nil
}

func (a *app) usageLine(name string) string {
	for _, c := range a.commands() {
		if c.name == name {
			return strings.TrimSpace(c.name + " [flags] " + c.args)
		}
	}
	return name
}

func (a *app) commandHelp(fs *flag.FlagSet) {
	fmt.Fprintf(a.stdout, "Usage: plaitway %s\n\nFlags:\n", a.usageLine(fs.Name()))
	fs.SetOutput(a.stdout)
	fs.PrintDefaults()
	fs.SetOutput(io.Discard)
}

// socketPath is the -socket flag, else $PLAITWAY_SOCKET, else the default.
func (a *app) socketPath() string {
	switch {
	case a.socket != "":
		return a.socket
	case a.getenv("PLAITWAY_SOCKET") != "":
		return a.getenv("PLAITWAY_SOCKET")
	}
	return defaultSocket
}

func (a *app) printVersion(_ context.Context, args []string) error {
	fs := a.flagSet("version")
	if _, err := a.parse(fs, args, 0, 0); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "plaitway %s\n", version)
	return nil
}
