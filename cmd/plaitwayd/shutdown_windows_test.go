package main

import (
	"context"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
)

// The service host promises the service manager that a stop is over within
// productionTiming.stopTimeout, and reports a stop that overran as a failure.
// A stop request with a tunnel up must remove its routes and DNS entries inside
// that budget and end as a clean exit, with the production timing and the
// daemon's own shutdown order.
func TestServiceStopRemovesTheRoutesAndDNSWithinTheAnnouncedBudget(t *testing.T) {
	for name, control := range map[string]svc.Cmd{"stop": svc.Stop, "shutdown": svc.Shutdown, "preshutdown": svc.PreShutdown} {
		t.Run(name, func(t *testing.T) {
			h := newHostedDaemon(t, slowDelete)
			h.announceFullTunnel(t)
			ready := make(chan struct{})
			close(ready)
			handler := &serviceHandler{
				log:         discardLog(),
				run:         func(ctx context.Context) error { return h.d.serve(ctx, h.lis) },
				ready:       ready,
				timing:      productionTiming,
				stopSignals: make(chan os.Signal),
			}
			r := &handlerRun{requests: make(chan svc.ChangeRequest), statuses: make(chan svc.Status, 1024), done: make(chan [2]uint32, 1)}
			go func() {
				specific, code := handler.Execute(nil, r.requests, r.statuses)
				r.done <- [2]uint32{boolToUint32(specific), code}
			}()
			r.waitForState(t, svc.Running)

			stopRequested := time.Now()
			r.send(t, control)
			specific, code := r.finishWithin(t, productionTiming.stopTimeout)

			if specific || code != 0 {
				t.Errorf("Execute returned (%v, %d), want a clean exit", specific, code)
			}
			if took := time.Since(stopRequested); took >= productionTiming.stopTimeout {
				t.Errorf("the stop took %s, the budget announced to the service manager is %s", took, productionTiming.stopTimeout)
			}
			h.requireHostRestored(t)
		})
	}
}

func boolToUint32(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

// finishWithin is finish with the wait of the caller's choosing: the budget of
// the service is longer than the default wait of the tests.
func (r *handlerRun) finishWithin(t *testing.T, wait time.Duration) (specific bool, code uint32) {
	t.Helper()
	select {
	case result := <-r.done:
		return result[0] == 1, result[1]
	case <-time.After(wait):
		t.Fatalf("Execute did not return within %s", wait)
		return false, 0
	}
}
