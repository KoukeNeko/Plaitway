package ovpn

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDialMgmtWaitsForTheSocket(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "late.sock")
	go func() {
		time.Sleep(150 * time.Millisecond)
		ln, err := net.Listen("unix", path)
		if err != nil {
			return
		}
		defer ln.Close()
		conn, err := ln.Accept()
		if err == nil {
			conn.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := dialMgmt(ctx, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}

func TestDialMgmtGivesUp(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "never.sock")

	t.Run("deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		if _, err := dialMgmt(ctx, path, nil); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("dialMgmt error = %v, want a deadline error", err)
		}
	})
	t.Run("child exited", func(t *testing.T) {
		abort := make(chan struct{})
		close(abort)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		start := time.Now()
		if _, err := dialMgmt(ctx, path, abort); err == nil || !strings.Contains(err.Error(), "exited") {
			t.Fatalf("dialMgmt error = %v, want the child-exited error", err)
		}
		if time.Since(start) > 2*time.Second {
			t.Error("dialMgmt kept waiting after the child exited")
		}
	})
}
