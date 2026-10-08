package main

import (
	"testing"

	"github.com/Microsoft/go-winio"
)

// systemOnlyPipeDescriptor lets nobody but SYSTEM open the pipe, not even the
// user who creates it.
const systemOnlyPipeDescriptor = "D:P(A;;GA;;;SY)"

// A pipe is there, but its access list keeps this user out; the daemon's own
// list lets in only SYSTEM, administrators and interactive users.
func TestDaemonPipeThatKeepsThisUserOut(t *testing.T) {
	t.Parallel()
	pipe := newSocketPath(t)
	listener, err := winio.ListenPipe(pipe, &winio.PipeConfig{SecurityDescriptor: systemOnlyPipeDescriptor})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })

	if r := runAt(t, pipe, "", nil, "status"); r.code != exitFailure || r.stderr != "plaitway: permission denied to open "+pipe+"\n" {
		t.Errorf("pipe of SYSTEM: %+v", r)
	}
}
