package linux

import (
	"fmt"
	"syscall"
)

// dumpAttempts: a dump that the kernel marked as interrupted by a change is
// asked for again.
const dumpAttempts = 5

// netlinkError is the status of a request the kernel refused, with its
// explanation when it gave one.
type netlinkError struct {
	Errno  syscall.Errno
	ExtAck string
}

func (e *netlinkError) Error() string {
	if e.ExtAck == "" {
		return e.Errno.Error()
	}
	return e.Errno.Error() + " (" + e.ExtAck + ")"
}

func (e *netlinkError) Unwrap() error { return e.Errno }

// newNetlinkError is the error for an NLMSG_ERROR or NLMSG_DONE message whose
// status is a failure.
func newNetlinkError(m rtMessage) *netlinkError {
	return &netlinkError{Errno: syscall.Errno(-m.Errno), ExtAck: m.ExtAck}
}

// readAck reads datagrams from recv until the kernel answers the request with
// this sequence number: nil for an acknowledgement, a *netlinkError for a
// refusal.
func readAck(seq uint32, recv func() ([]byte, error)) error {
	for {
		b, err := recv()
		if err != nil {
			return err
		}
		msgs, err := parseMessages(b)
		if err != nil {
			return err
		}
		for _, m := range msgs {
			if m.Seq != seq || m.Type != nlmsgError {
				continue
			}
			if m.Errno == 0 {
				return nil
			}
			return newNetlinkError(m)
		}
	}
}

// readDump reads datagrams from recv until the kernel ends the dump with this
// sequence number, and returns the link, address and route messages in it.
// interrupted is set when the kernel says that the table changed while it was
// being dumped.
func readDump(seq uint32, typ uint16, recv func() ([]byte, error)) (msgs []rtMessage, interrupted bool, err error) {
	for {
		b, err := recv()
		if err != nil {
			return nil, false, err
		}
		batch, err := parseMessages(b)
		if err != nil {
			return nil, false, err
		}
		for _, m := range batch {
			if m.Seq != seq {
				continue
			}
			interrupted = interrupted || m.Flags&nlmFDumpIntr != 0
			switch {
			case (m.Type == nlmsgError || m.Type == nlmsgDone) && m.Errno != 0:
				return nil, false, fmt.Errorf("dump netlink type %d: %w", typ, newNetlinkError(m))
			case m.Type == nlmsgDone:
				return msgs, interrupted, nil
			case m.Route != nil || m.Link != nil || m.Addr != nil:
				msgs = append(msgs, m)
			}
		}
	}
}

// dumpConsistently asks for a dump until the kernel gives one that no change
// interrupted. Entries of an interrupted dump may be missing or repeated, and a
// table that keeps changing is an error rather than a table that may be wrong.
func dumpConsistently(typ uint16, once func() ([]rtMessage, bool, error)) ([]rtMessage, error) {
	for range dumpAttempts {
		msgs, interrupted, err := once()
		if err != nil {
			return nil, err
		}
		if !interrupted {
			return msgs, nil
		}
	}
	return nil, fmt.Errorf("dump netlink type %d: the kernel changed it %d times while it was read", typ, dumpAttempts)
}
