package linux

import (
	"errors"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// datagrams is a socket that hands out what the test put in it, and then fails.
func datagrams(parts ...[]byte) func() ([]byte, error) {
	return func() ([]byte, error) {
		if len(parts) == 0 {
			return nil, errors.New("nothing more to read")
		}
		next := parts[0]
		parts = parts[1:]
		return next, nil
	}
}

var (
	d0Route = routePayload(afInet, 24, rtTableMain, rtprotKernel, rtScopeLink, rtnUnicast, 0, attrOf(rtaDst, []byte{192, 0, 2, 0}), attrOf(rtaOif, le32(2)))
	d0Link  = append(append([]byte{0, 0, 1, 0}, le32(2)...), append(le32(iffUp|iffLowerUp), append(le32(0), attrOf(iflaIfName, []byte("d0\x00"))...)...)...)
)

func TestReadAck(t *testing.T) {
	request := message(rtmNewRoute, nlmFRequest|nlmFAck, 7, d0Route)
	ack := message(nlmsgError, 0, 7, le32(0), request)
	refusal := message(nlmsgError, nlmFAckTLVs, 7, le32(uint32(0xffffff9b)), request, attrOf(nlmsgErrAttrMsg, []byte("Nexthop has invalid gateway\x00")))

	if err := readAck(7, datagrams(ack)); err != nil {
		t.Errorf("acknowledgement: %v", err)
	}
	// Answers to other requests are not ours.
	if err := readAck(7, datagrams(message(nlmsgError, 0, 6, le32(uint32(0xffffffef)), request), ack)); err != nil {
		t.Errorf("acknowledgement after the answer to another request: %v", err)
	}
	// Notifications that come on the socket meanwhile are not answers.
	if err := readAck(7, datagrams(message(rtmNewRoute, 0, 0, d0Route), ack)); err != nil {
		t.Errorf("acknowledgement after a notification: %v", err)
	}

	err := readAck(7, datagrams(refusal))
	var refused *netlinkError
	if !errors.As(err, &refused) || refused.Errno != 101 || refused.ExtAck != "Nexthop has invalid gateway" {
		t.Fatalf("refusal: %v", err)
	}
	if want := syscall.Errno(101).Error() + " (Nexthop has invalid gateway)"; err.Error() != want {
		t.Errorf("error text %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, syscall.Errno(101)) {
		t.Errorf("the errno is lost: %v", err)
	}

	if err := readAck(7, datagrams([]byte{1, 2, 3})); err == nil {
		t.Error("garbage was taken for an answer")
	}
	if err := readAck(7, datagrams()); err == nil || !strings.Contains(err.Error(), "nothing more") {
		t.Errorf("a socket that fails: %v", err)
	}
}

func TestReadDump(t *testing.T) {
	route := func(seq uint32, flags uint16) []byte { return message(rtmNewRoute, flags, seq, d0Route) }
	done := func(seq uint32, status uint32, flags uint16) []byte {
		return message(nlmsgDone, 2|flags, seq, le32(status))
	}

	t.Run("several datagrams", func(t *testing.T) {
		recv := datagrams(slices.Concat(route(5, 2), route(5, 2)), route(5, 2), done(5, 0, 0))
		msgs, interrupted, err := readDump(5, rtmGetRoute, recv)
		if err != nil || interrupted || len(msgs) != 3 || msgs[0].Route == nil {
			t.Errorf("got %d messages, interrupted %v, err %v", len(msgs), interrupted, err)
		}
	})
	t.Run("an empty dump", func(t *testing.T) {
		msgs, interrupted, err := readDump(5, rtmGetRoute, datagrams(done(5, 0, 0)))
		if err != nil || interrupted || len(msgs) != 0 {
			t.Errorf("got %d messages, interrupted %v, err %v", len(msgs), interrupted, err)
		}
	})
	t.Run("only the dump asked for", func(t *testing.T) {
		recv := datagrams(slices.Concat(route(4, 2), route(5, 2)), done(4, 0, 0), done(5, 0, 0))
		msgs, _, err := readDump(5, rtmGetRoute, recv)
		if err != nil || len(msgs) != 1 {
			t.Errorf("got %d messages, err %v", len(msgs), err)
		}
	})
	t.Run("links, addresses and routes", func(t *testing.T) {
		recv := datagrams(slices.Concat(message(rtmNewLink, 2, 5, d0Link), message(rtmNewRoute, 2, 5, d0Route)), done(5, 0, 0))
		msgs, _, err := readDump(5, rtmGetLink, recv)
		if err != nil || len(msgs) != 2 || msgs[0].Link == nil || msgs[1].Route == nil {
			t.Errorf("got %+v, err %v", msgs, err)
		}
	})
	t.Run("interrupted", func(t *testing.T) {
		// The flag is on one message of the dump, whichever the kernel was at.
		msgs, interrupted, err := readDump(5, rtmGetRoute, datagrams(route(5, 2), route(5, 2|nlmFDumpIntr), done(5, 0, 0)))
		if err != nil || !interrupted || len(msgs) != 2 {
			t.Errorf("got %d messages, interrupted %v, err %v", len(msgs), interrupted, err)
		}
		_, interrupted, err = readDump(5, rtmGetRoute, datagrams(route(5, 2), done(5, 0, nlmFDumpIntr)))
		if err != nil || !interrupted {
			t.Errorf("flag on the done message: interrupted %v, err %v", interrupted, err)
		}
	})
	t.Run("a dump the kernel could not finish", func(t *testing.T) {
		_, _, err := readDump(5, rtmGetRoute, datagrams(route(5, 2), done(5, uint32(0xffffffea), 0)))
		if !errors.Is(err, syscall.Errno(22)) {
			t.Errorf("err %v", err)
		}
	})
	t.Run("a dump the kernel refused", func(t *testing.T) {
		request := message(rtmGetRoute, nlmFRequest|nlmFDump, 5, make([]byte, rtMsgLen))
		_, _, err := readDump(5, rtmGetRoute, datagrams(message(nlmsgError, 0, 5, le32(uint32(0xffffffa1)), request)))
		if !errors.Is(err, syscall.Errno(95)) {
			t.Errorf("err %v", err)
		}
	})
	t.Run("garbage", func(t *testing.T) {
		if _, _, err := readDump(5, rtmGetRoute, datagrams(route(5, 2), []byte{1, 2, 3})); err == nil {
			t.Error("no error")
		}
	})
	t.Run("a socket that fails halfway", func(t *testing.T) {
		if _, _, err := readDump(5, rtmGetRoute, datagrams(route(5, 2))); err == nil {
			t.Error("no error")
		}
	})
}

func TestDumpConsistently(t *testing.T) {
	good := []rtMessage{{Type: rtmNewRoute}}
	t.Run("a dump nobody interrupted", func(t *testing.T) {
		calls := 0
		got, err := dumpConsistently(rtmGetRoute, func() ([]rtMessage, bool, error) { calls++; return good, false, nil })
		if err != nil || len(got) != 1 || calls != 1 {
			t.Errorf("got %v, err %v after %d calls", got, err, calls)
		}
	})
	t.Run("asks again after an interruption", func(t *testing.T) {
		calls := 0
		got, err := dumpConsistently(rtmGetRoute, func() ([]rtMessage, bool, error) {
			calls++
			if calls < 3 {
				return []rtMessage{{}, {}}, true, nil // incomplete, and not to be used
			}
			return good, false, nil
		})
		if err != nil || len(got) != 1 || calls != 3 {
			t.Errorf("got %v, err %v after %d calls", got, err, calls)
		}
	})
	t.Run("gives up on a table that keeps changing", func(t *testing.T) {
		calls := 0
		got, err := dumpConsistently(rtmGetRoute, func() ([]rtMessage, bool, error) { calls++; return good, true, nil })
		if err == nil || got != nil || calls != dumpAttempts {
			t.Errorf("got %v, err %v after %d calls", got, err, calls)
		}
	})
	t.Run("an error ends it", func(t *testing.T) {
		boom := errors.New("boom")
		calls := 0
		_, err := dumpConsistently(rtmGetRoute, func() ([]rtMessage, bool, error) { calls++; return nil, false, boom })
		if !errors.Is(err, boom) || calls != 1 {
			t.Errorf("err %v after %d calls", err, calls)
		}
	})
}
