package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
)

const (
	defaultLogLines = 200
	// logHistoryIdle is how long a log stream must stay quiet before the
	// buffered history is taken to be over: the stream sends the history and
	// then the live lines without a marker between them.
	logHistoryIdle = 500 * time.Millisecond
	timeLayout     = "2006-01-02 15:04:05"
)

var errInterrupted = errors.New("interrupted")

func (a *app) status(ctx context.Context, args []string) error {
	fs := a.flagSet("status")
	asJSON := fs.Bool("json", false, "print JSON")
	rest, err := a.parse(fs, args, 0, 1)
	if err != nil {
		return err
	}
	c, err := a.dial()
	if err != nil {
		return err
	}
	defer c.close()
	profiles, err := c.profiles(ctx)
	if err != nil {
		return err
	}
	if len(rest) == 1 {
		p, err := matchProfile(profiles, rest[0])
		if err != nil {
			return err
		}
		profiles = []*pb.Profile{p}
	}
	if *asJSON {
		return a.printJSON(&pb.ListProfilesResponse{Profiles: profiles})
	}

	now := time.Now()
	rows := make([][]string, len(profiles))
	for i, p := range profiles {
		st := p.GetStatus()
		uptime, rx, tx := "-", "-", "-"
		if st.GetConnectedSince() != nil {
			uptime = humanDuration(now.Sub(st.ConnectedSince.AsTime()))
		}
		if st.GetInterfaceName() != "" {
			rx, tx = humanBytes(st.RxBytes), humanBytes(st.TxBytes)
		}
		rows[i] = []string{p.Name, stateName(p.State), kindName(p.Kind), dash(st.GetInterfaceName()),
			dash(strings.Join(st.GetAddresses(), ",")), uptime, rx, tx, p.LastError}
	}
	a.table([]string{"NAME", "STATE", "KIND", "INTERFACE", "ADDRESSES", "UPTIME", "RX", "TX", "ERROR"}, rows,
		func(row, col int, cell string) string {
			if col == 1 {
				return a.paintState(profiles[row].State, cell)
			}
			return cell
		})
	if len(rest) == 1 {
		a.printSettings(profiles[0])
	}
	return nil
}

func (a *app) list(ctx context.Context, args []string) error {
	fs := a.flagSet("list")
	asJSON := fs.Bool("json", false, "print JSON")
	if _, err := a.parse(fs, args, 0, 0); err != nil {
		return err
	}
	c, err := a.dial()
	if err != nil {
		return err
	}
	defer c.close()
	profiles, err := c.profiles(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		return a.printJSON(&pb.ListProfilesResponse{Profiles: profiles})
	}

	rows := make([][]string, len(profiles))
	for i, p := range profiles {
		rows[i] = []string{p.Id, p.Name, kindName(p.Kind), stateName(p.State)}
	}
	a.table([]string{"ID", "NAME", "KIND", "STATE"}, rows, func(row, col int, cell string) string {
		if col == 3 {
			return a.paintState(profiles[row].State, cell)
		}
		return cell
	})
	return nil
}

func (a *app) disconnect(ctx context.Context, args []string) error {
	fs := a.flagSet("disconnect")
	rest, err := a.parse(fs, args, 1, 1)
	if err != nil {
		return err
	}
	c, err := a.dial()
	if err != nil {
		return err
	}
	defer c.close()
	p, err := c.find(ctx, rest[0])
	if err != nil {
		return err
	}
	stopped, err := c.setEnabled(ctx, p.Id, false)
	if err != nil {
		return err
	}
	// The daemon reports an engine that would not stop as FAILED.
	if stopped.State == pb.ProfileState_PROFILE_STATE_FAILED {
		return fmt.Errorf("%s: could not disconnect: %s", p.Name, stopped.LastError)
	}
	fmt.Fprintf(a.stdout, "%s: disconnected\n", clean(p.Name))
	return nil
}

func (c *client) setEnabled(ctx context.Context, id string, enabled bool) (*pb.Profile, error) {
	ctx, cancel := callContext(ctx)
	defer cancel()
	p, err := c.api.SetProfileEnabled(ctx, &pb.SetProfileEnabledRequest{Id: id, Enabled: enabled})
	if err != nil {
		return nil, c.failure(err)
	}
	return p, nil
}

func (a *app) remove(ctx context.Context, args []string) error {
	fs := a.flagSet("remove")
	rest, err := a.parse(fs, args, 1, 1)
	if err != nil {
		return err
	}
	c, err := a.dial()
	if err != nil {
		return err
	}
	defer c.close()
	p, err := c.find(ctx, rest[0])
	if err != nil {
		return err
	}
	callCtx, cancel := callContext(ctx)
	defer cancel()
	if _, err := c.api.DeleteProfile(callCtx, &pb.DeleteProfileRequest{Id: p.Id}); err != nil {
		return c.failure(err)
	}
	fmt.Fprintf(a.stdout, "removed %s\n", clean(p.Name))
	return nil
}

func (a *app) resync(ctx context.Context, args []string) error {
	fs := a.flagSet("resync")
	if _, err := a.parse(fs, args, 0, 0); err != nil {
		return err
	}
	c, err := a.dial()
	if err != nil {
		return err
	}
	defer c.close()
	callCtx, cancel := callContext(ctx)
	defer cancel()
	if _, err := c.api.Resync(callCtx, &pb.ResyncRequest{}); err != nil {
		return c.failure(err)
	}
	fmt.Fprintln(a.stdout, "resynced")
	return nil
}

func (a *app) logs(ctx context.Context, args []string) error {
	fs := a.flagSet("logs")
	follow := fs.Bool("f", false, "keep printing new lines until interrupted")
	lines := fs.Int("n", defaultLogLines, "number of earlier lines to show")
	asJSON := fs.Bool("json", false, "print one JSON object per line")
	rest, err := a.parse(fs, args, 0, 1)
	if err != nil {
		return err
	}
	if *lines < 1 {
		return usageErrorf("logs: -n must be at least 1")
	}
	c, err := a.dial()
	if err != nil {
		return err
	}
	defer c.close()
	var id string // the daemon's own log when empty
	if len(rest) == 1 {
		p, err := c.find(ctx, rest[0])
		if err != nil {
			return err
		}
		id = p.Id
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := c.api.WatchLogs(streamCtx, &pb.WatchLogsRequest{ProfileId: id, TailLines: int32(min(*lines, math.MaxInt32))})
	if err != nil {
		return c.failure(err)
	}
	received := make(chan *pb.LogLine)
	failed := make(chan error, 1)
	go func() {
		for {
			line, err := stream.Recv()
			if err != nil {
				failed <- err
				return
			}
			select {
			case received <- line:
			case <-streamCtx.Done():
				return
			}
		}
	}()

	idle := time.NewTimer(logHistoryIdle)
	defer idle.Stop()
	if *follow {
		idle.Stop()
	}
	for {
		select {
		case line := <-received:
			if err := a.printLog(line, *asJSON); err != nil {
				return err
			}
			if !*follow {
				idle.Reset(logHistoryIdle)
			}
		case <-idle.C:
			return nil
		case err := <-failed:
			return a.streamEnded(ctx, c, err, *follow)
		case <-ctx.Done():
			return a.streamEnded(ctx, c, ctx.Err(), *follow)
		}
	}
}

// streamEnded is the result of a command that reads a stream: stopping a
// stream that is meant to run until interrupted is success.
func (a *app) streamEnded(ctx context.Context, c *client, err error, untilInterrupted bool) error {
	switch {
	case ctx.Err() != nil && untilInterrupted:
		return nil
	case ctx.Err() != nil:
		return errInterrupted
	case errors.Is(err, io.EOF):
		return errors.New("the daemon closed the stream")
	}
	return c.failure(err)
}

func (a *app) printLog(l *pb.LogLine, asJSON bool) error {
	if asJSON {
		return a.printJSONLine(l)
	}
	level := strings.TrimPrefix(l.Level.String(), "LOG_LEVEL_")
	fmt.Fprintf(a.stdout, "%s  %-5s  %s\n", formatTime(l.Time), level, clean(l.Text))
	return nil
}

func formatTime(ts *timestamppb.Timestamp) string {
	if ts == nil {
		return ""
	}
	return ts.AsTime().Local().Format(timeLayout)
}

func (a *app) watch(ctx context.Context, args []string) error {
	fs := a.flagSet("watch")
	asJSON := fs.Bool("json", false, "print one JSON object per line")
	if _, err := a.parse(fs, args, 0, 0); err != nil {
		return err
	}
	c, err := a.dial()
	if err != nil {
		return err
	}
	defer c.close()
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := c.api.WatchProfiles(streamCtx, &pb.WatchProfilesRequest{})
	if err != nil {
		return c.failure(err)
	}

	known := map[string]watched{}
	for {
		ev, err := stream.Recv()
		if err != nil {
			return a.streamEnded(ctx, c, err, true)
		}
		switch e := ev.Event.(type) {
		case *pb.ProfileEvent_Snapshot:
			if *asJSON {
				if err := a.printJSONLine(ev); err != nil {
					return err
				}
			}
			for _, p := range e.Snapshot.Profiles {
				known[p.Id] = watched{name: p.Name, signature: watchSignature(p)}
				if !*asJSON {
					a.printWatchLine(p)
				}
			}
		case *pb.ProfileEvent_Changed:
			// The traffic counters move with every poll; only a change that
			// a person would call one is printed.
			p := e.Changed
			signature := watchSignature(p)
			if known[p.Id].signature == signature {
				continue
			}
			known[p.Id] = watched{name: p.Name, signature: signature}
			if *asJSON {
				err = a.printJSONLine(ev)
			} else {
				a.printWatchLine(p)
			}
			if err != nil {
				return err
			}
		case *pb.ProfileEvent_Removed:
			name := e.Removed
			if w, ok := known[e.Removed]; ok {
				name = w.name
			}
			delete(known, e.Removed)
			if *asJSON {
				if err := a.printJSONLine(ev); err != nil {
					return err
				}
			} else {
				fmt.Fprintf(a.stdout, "%s  %s  removed\n", time.Now().Format(time.TimeOnly), clean(name))
			}
		}
	}
}

// watched is what watch has printed about a profile.
type watched struct{ name, signature string }

// watchSignature is what makes a profile's change worth printing.
func watchSignature(p *pb.Profile) string {
	st := p.GetStatus()
	return fmt.Sprint(p.Name, p.State, p.DesiredEnabled, p.LastError, p.GetCredentialRequest().GetKind(),
		st.GetInterfaceName(), st.GetAddresses())
}

func (a *app) printWatchLine(p *pb.Profile) {
	st := p.GetStatus()
	fields := []string{time.Now().Format(time.TimeOnly), clean(p.Name), a.paintState(p.State, stateName(p.State))}
	if st.GetInterfaceName() != "" {
		fields = append(fields, clean(st.InterfaceName))
	}
	if len(st.GetAddresses()) > 0 {
		fields = append(fields, clean(strings.Join(st.Addresses, ",")))
	}
	if p.LastError != "" {
		fields = append(fields, "error: "+clean(p.LastError))
	}
	fmt.Fprintln(a.stdout, strings.Join(fields, "  "))
}
