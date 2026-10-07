package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
)

// scripted is a daemon whose answers a test writes. The real daemon cannot be
// made to refuse the user running the tests, to hold two profiles of one name
// or to ask for a key passphrase; this one can. A method without a hook fails
// as the daemon does for a method it lacks.
type scripted struct {
	pb.UnimplementedDaemonServiceServer

	list          func() ([]*pb.Profile, error)
	enable        func(*pb.SetProfileEnabledRequest) (*pb.Profile, error)
	provide       func(*pb.ProvideCredentialsRequest) (*pb.Profile, error)
	importFn      func(*pb.ImportProfileRequest) (*pb.ImportProfileResponse, error)
	update        func(*pb.UpdateProfileRequest) (*pb.Profile, error)
	getContent    func(*pb.GetProfileContentRequest) (*pb.GetProfileContentResponse, error)
	updateContent func(*pb.UpdateProfileContentRequest) (*pb.ImportProfileResponse, error)
	remove        func(*pb.DeleteProfileRequest) error
	watch         func(stream grpc.ServerStreamingServer[pb.ProfileEvent]) error
}

func (s *scripted) ListProfiles(context.Context, *pb.ListProfilesRequest) (*pb.ListProfilesResponse, error) {
	if s.list == nil {
		return &pb.ListProfilesResponse{}, nil
	}
	profiles, err := s.list()
	return &pb.ListProfilesResponse{Profiles: profiles}, err
}

func (s *scripted) SetProfileEnabled(_ context.Context, req *pb.SetProfileEnabledRequest) (*pb.Profile, error) {
	return s.enable(req)
}

func (s *scripted) ProvideCredentials(_ context.Context, req *pb.ProvideCredentialsRequest) (*pb.Profile, error) {
	return s.provide(req)
}

func (s *scripted) ImportProfile(_ context.Context, req *pb.ImportProfileRequest) (*pb.ImportProfileResponse, error) {
	return s.importFn(req)
}

func (s *scripted) UpdateProfile(_ context.Context, req *pb.UpdateProfileRequest) (*pb.Profile, error) {
	return s.update(req)
}

func (s *scripted) GetProfileContent(_ context.Context, req *pb.GetProfileContentRequest) (*pb.GetProfileContentResponse, error) {
	return s.getContent(req)
}

func (s *scripted) UpdateProfileContent(_ context.Context, req *pb.UpdateProfileContentRequest) (*pb.ImportProfileResponse, error) {
	return s.updateContent(req)
}

func (s *scripted) DeleteProfile(_ context.Context, req *pb.DeleteProfileRequest) (*pb.DeleteProfileResponse, error) {
	return &pb.DeleteProfileResponse{}, s.remove(req)
}

func (s *scripted) WatchProfiles(_ *pb.WatchProfilesRequest, stream grpc.ServerStreamingServer[pb.ProfileEvent]) error {
	return s.watch(stream)
}

// serve starts the scripted daemon and returns the socket it listens on.
func (s *scripted) serve(t *testing.T) string {
	t.Helper()
	socket := filepath.Join(shortDir(t), "s.sock")
	// Not transport.Listen: it narrows the umask of the whole process while it
	// binds, and a directory another parallel test creates meanwhile has no
	// permission to be entered.
	lis, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	pb.RegisterDaemonServiceServer(srv, s)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	return socket
}

// run executes a command line against the socket, without a terminal.
func runAt(t *testing.T, socket, stdin string, tweak func(*app), args ...string) result {
	t.Helper()
	return runAtContext(t, context.Background(), socket, stdin, tweak, args...)
}

func runAtContext(t *testing.T, ctx context.Context, socket, stdin string, tweak func(*app), args ...string) result {
	t.Helper()
	var stdout, stderr syncBuffer
	a := &app{stdin: strings.NewReader(stdin), stdout: &stdout, stderr: &stderr, getenv: func(string) string { return "" }, socket: socket}
	if tweak != nil {
		tweak(a)
	}
	code := a.run(ctx, args)
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func profileOf(id, name string, state pb.ProfileState) *pb.Profile {
	return &pb.Profile{Id: id, Name: name, State: state}
}

func snapshot(profiles ...*pb.Profile) *pb.ProfileEvent {
	return &pb.ProfileEvent{Event: &pb.ProfileEvent_Snapshot{Snapshot: &pb.ProfileSnapshot{Profiles: profiles}}}
}

func TestDaemonErrorsAreWorded(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		code    codes.Code
		message string
		want    string
	}{
		{codes.PermissionDenied, "uid 501 is neither the console user nor an administrator", "plaitway: permission denied: uid 501 is neither the console user nor an administrator\n"},
		{codes.Unavailable, "daemon shutting down", "plaitway: the daemon is unavailable: daemon shutting down\n"},
		{codes.NotFound, `profile "x": not found`, "plaitway: not found: profile \"x\": not found\n"},
		{codes.InvalidArgument, "name is empty", "plaitway: name is empty\n"},
		{codes.Internal, "disk full", "plaitway: disk full (Internal)\n"},
		{codes.DeadlineExceeded, "", "plaitway: the daemon did not answer in time\n"},
		// Text of an engine reaches the terminal through the daemon.
		{codes.Internal, "login refused\x1b[2J\x07", "plaitway: login refused\uFFFD[2J\uFFFD (Internal)\n"},
	} {
		socket := (&scripted{list: func() ([]*pb.Profile, error) { return nil, status.Error(c.code, c.message) }}).serve(t)
		if r := runAt(t, socket, "", nil, "list"); r.code != exitFailure || r.stdout != "" || r.stderr != c.want {
			t.Errorf("%v %q: exit %d, stdout %q, stderr %q; want stderr %q", c.code, c.message, r.code, r.stdout, r.stderr, c.want)
		}
	}
}

func TestDaemonThatIsNotRunning(t *testing.T) {
	t.Parallel()
	dir := shortDir(t)

	missing := filepath.Join(dir, "missing", "d.sock")
	if r := runAt(t, missing, "", nil, "status"); r.code != exitFailure || r.stderr != "plaitway: the daemon is not running: "+missing+" does not exist\n" {
		t.Errorf("no socket: %+v", r)
	}

	// A daemon that was killed leaves its socket file behind.
	stale := filepath.Join(dir, "stale.sock")
	lis, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	lis.(*net.UnixListener).SetUnlinkOnClose(false)
	lis.Close()
	if r := runAt(t, stale, "", nil, "status"); r.code != exitFailure || r.stderr != "plaitway: the daemon is not running: nothing listens on "+stale+"\n" {
		t.Errorf("stale socket: %+v", r)
	}

	if os.Getuid() != 0 {
		locked := filepath.Join(dir, "locked")
		if err := os.Mkdir(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(locked, 0o700)
		socket := filepath.Join(locked, "d.sock")
		if r := runAt(t, socket, "", nil, "status"); r.code != exitFailure || r.stderr != "plaitway: permission denied to open "+socket+"\n" {
			t.Errorf("socket in a closed directory: %+v", r)
		}
	}

	// A path that is no socket, and one macOS cannot bind.
	notSocket := filepath.Join(dir, "file")
	if err := os.WriteFile(notSocket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if r := runAt(t, notSocket, "", nil, "status"); r.code != exitFailure || !strings.Contains(r.stderr, "cannot connect to "+notSocket+": ") {
		t.Errorf("a file that is no socket: %+v", r)
	}
}

func TestSocketPathPrecedence(t *testing.T) {
	t.Parallel()
	env := func(value string) func(string) string {
		return func(name string) string {
			if name == "PLAITWAY_SOCKET" {
				return value
			}
			return ""
		}
	}
	for _, c := range []struct{ flag, env, want string }{
		{"", "", defaultSocket},
		{"", "/env.sock", "/env.sock"},
		{"/flag.sock", "", "/flag.sock"},
		{"/flag.sock", "/env.sock", "/flag.sock"},
	} {
		a := &app{getenv: env(c.env), socket: c.flag}
		if got := a.socketPath(); got != c.want {
			t.Errorf("flag %q, env %q: %q, want %q", c.flag, c.env, got, c.want)
		}
	}
	if defaultSocket != "/var/run/plaitway/plaitwayd.sock" {
		t.Errorf("the default socket is %s", defaultSocket)
	}
}

// -socket is accepted before the command and after it, and a relative path
// names the file, not a host.
func TestSocketFlagPositionAndRelativePath(t *testing.T) {
	// Not parallel: it changes the working directory.
	socket := (&scripted{}).serve(t)
	t.Chdir(filepath.Dir(socket))

	for _, args := range [][]string{
		{"-socket", "s.sock", "list"},
		{"list", "-socket", "s.sock"},
		{"list", "-socket=s.sock"},
		{"-socket", socket, "list"},
	} {
		a := &app{stdin: strings.NewReader(""), stdout: &syncBuffer{}, stderr: &syncBuffer{}, getenv: func(string) string { return "" }}
		var stderr syncBuffer
		a.stderr = &stderr
		if code := a.run(context.Background(), args); code != 0 {
			t.Errorf("%v: exit %d, %s", args, code, stderr.String())
		}
	}
}

func TestAmbiguousNameIsAnErrorForEveryCommand(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var changed []string
	record := func(what string) {
		mu.Lock()
		defer mu.Unlock()
		changed = append(changed, what)
	}
	socket := (&scripted{
		list: func() ([]*pb.Profile, error) {
			return []*pb.Profile{
				profileOf("ID-UPPER", "VPN", pb.ProfileState_PROFILE_STATE_DISCONNECTED),
				profileOf("ID-LOWER", "vpn", pb.ProfileState_PROFILE_STATE_DISCONNECTED),
				profileOf("ID-OTHER", "other", pb.ProfileState_PROFILE_STATE_DISCONNECTED),
			}, nil
		},
		enable: func(req *pb.SetProfileEnabledRequest) (*pb.Profile, error) {
			record("enable " + req.Id)
			return profileOf(req.Id, "VPN", pb.ProfileState_PROFILE_STATE_DISCONNECTED), nil
		},
		remove: func(req *pb.DeleteProfileRequest) error { record("remove " + req.Id); return nil },
	}).serve(t)

	for _, command := range []string{"connect", "disconnect", "remove", "status", "logs"} {
		r := runAt(t, socket, "", nil, command, "Vpn")
		want := `plaitway: "Vpn" fits 2 profiles, use an id: ID-UPPER (VPN), ID-LOWER (vpn)` + "\n"
		if r.code != exitFailure || r.stdout != "" || r.stderr != want {
			t.Errorf("%s: %+v", command, r)
		}
	}
	if len(changed) != 0 {
		t.Errorf("a refused command changed %v", changed)
	}

	// An id is exact, and so is a name that only one profile has.
	if r := runAt(t, socket, "", nil, "disconnect", "ID-LOWER"); r.code != 0 {
		t.Errorf("disconnect by id: %+v", r)
	}
	if r := runAt(t, socket, "", nil, "remove", "OTHER"); r.code != 0 || r.stdout != "removed other\n" {
		t.Errorf("remove by name: %+v", r)
	}
	if len(changed) != 2 || changed[0] != "enable ID-LOWER" || changed[1] != "remove ID-OTHER" {
		t.Errorf("changed %v", changed)
	}
}

func TestTextFromTheDaemonCannotActOnTheTerminal(t *testing.T) {
	t.Parallel()
	socket := (&scripted{
		list: func() ([]*pb.Profile, error) {
			p := profileOf("ID", "evil\x1b]0;title\x07name", pb.ProfileState_PROFILE_STATE_FAILED)
			p.LastError = "server says \x1b[2J\x1b[Hhello"
			return []*pb.Profile{p}, nil
		},
	}).serve(t)

	for _, command := range []string{"status", "list"} {
		r := runAt(t, socket, "", nil, command)
		if r.code != 0 || strings.ContainsAny(r.stdout, "\x1b\x07") || !strings.Contains(r.stdout, "evil�]0;title�name") {
			t.Errorf("%s: %q", command, r.stdout)
		}
	}
	// Control characters are escaped by JSON itself.
	if r := runAt(t, socket, "", nil, "status", "-json"); strings.ContainsAny(r.stdout, "\x1b\x07") {
		t.Errorf("status -json: %q", r.stdout)
	}
}

func TestDisconnectReportsAnEngineThatWillNotStop(t *testing.T) {
	t.Parallel()
	socket := (&scripted{
		list: func() ([]*pb.Profile, error) {
			return []*pb.Profile{profileOf("ID", "home", pb.ProfileState_PROFILE_STATE_CONNECTED)}, nil
		},
		enable: func(*pb.SetProfileEnabledRequest) (*pb.Profile, error) {
			p := profileOf("ID", "home", pb.ProfileState_PROFILE_STATE_FAILED)
			p.LastError = "could not stop: timed out"
			return p, nil
		},
	}).serve(t)
	if r := runAt(t, socket, "", nil, "disconnect", "home"); r.code != exitFailure || r.stdout != "" ||
		r.stderr != "plaitway: home: could not disconnect: could not stop: timed out\n" {
		t.Errorf("disconnect: %+v", r)
	}
}

// connecting is a scripted daemon with one profile, whose watch stream answers
// from a list of snapshots, one per connection; the last one is repeated.
func connecting(t *testing.T, states ...*pb.Profile) (socket string, enabled func() []bool, provided func() []*pb.ProvideCredentialsRequest) {
	t.Helper()
	var (
		mu    sync.Mutex
		calls []bool
		sent  []*pb.ProvideCredentialsRequest
		seen  int
	)
	socket = (&scripted{
		list: func() ([]*pb.Profile, error) { return []*pb.Profile{states[0]}, nil },
		enable: func(req *pb.SetProfileEnabledRequest) (*pb.Profile, error) {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, req.Enabled)
			return profileOf(req.Id, "home", pb.ProfileState_PROFILE_STATE_CONNECTING), nil
		},
		provide: func(req *pb.ProvideCredentialsRequest) (*pb.Profile, error) {
			mu.Lock()
			defer mu.Unlock()
			sent = append(sent, req)
			return profileOf(req.ProfileId, "home", pb.ProfileState_PROFILE_STATE_CONNECTING), nil
		},
		watch: func(stream grpc.ServerStreamingServer[pb.ProfileEvent]) error {
			mu.Lock()
			state := states[min(seen, len(states)-1)]
			seen++
			mu.Unlock()
			if err := stream.Send(snapshot(state)); err != nil {
				return err
			}
			// As the daemon ends a watch whose client is gone or out of time.
			<-stream.Context().Done()
			return status.FromContextError(stream.Context().Err()).Err()
		},
	}).serve(t)
	return socket,
		func() []bool { mu.Lock(); defer mu.Unlock(); return append([]bool(nil), calls...) },
		func() []*pb.ProvideCredentialsRequest {
			mu.Lock()
			defer mu.Unlock()
			return append([]*pb.ProvideCredentialsRequest(nil), sent...)
		}
}

func TestConnectAsksForTheKeyPassphraseOnly(t *testing.T) {
	t.Parallel()
	asking := profileOf("ID", "home", pb.ProfileState_PROFILE_STATE_AWAITING_CREDENTIALS)
	asking.CredentialRequest = &pb.CredentialRequest{Kind: pb.CredentialKind_CREDENTIAL_KIND_KEY_PASSPHRASE}
	up := profileOf("ID", "home", pb.ProfileState_PROFILE_STATE_CONNECTED)
	up.Status = &pb.TunnelStatus{InterfaceName: "utun7", Addresses: []string{"10.0.0.2/24", "fd00::2/64"}}
	socket, _, provided := connecting(t, asking, up)

	// A user name given for a profile that does not ask for one is not sent.
	r := runAt(t, socket, "the phrase\r\n", nil, "connect", "-username", "alice", "home")
	if r.code != 0 || r.stdout != "home: connected (utun7, 10.0.0.2/24, fd00::2/64)\n" {
		t.Errorf("connect: %+v", r)
	}
	sent := provided()
	if len(sent) != 1 || sent[0].ProfileId != "ID" || sent[0].Kind != pb.CredentialKind_CREDENTIAL_KIND_KEY_PASSPHRASE ||
		sent[0].Username != "" || sent[0].Password != "the phrase" {
		t.Errorf("sent %v", sent)
	}

	// At a terminal the question names what is asked for.
	var question string
	asks := func(a *app) {
		a.prompt = func(_ context.Context, label string, secret bool) (string, error) {
			question = label
			return "phrase", nil
		}
	}
	socket, _, _ = connecting(t, asking, up)
	if r := runAt(t, socket, "", asks, "connect", "home"); r.code != 0 || question != "Key passphrase for home: " {
		t.Errorf("connect at a terminal: %+v, asked %q", r, question)
	}
}

func TestConnectStopsAskingWhenEveryAnswerIsRefused(t *testing.T) {
	t.Parallel()
	// The server says why, or says nothing.
	for _, reason := range []string{"authentication failed", ""} {
		asking := profileOf("ID", "home", pb.ProfileState_PROFILE_STATE_AWAITING_CREDENTIALS)
		asking.CredentialRequest = &pb.CredentialRequest{Kind: pb.CredentialKind_CREDENTIAL_KIND_USER_PASSWORD}
		refused := profileOf("ID", "home", pb.ProfileState_PROFILE_STATE_AWAITING_CREDENTIALS)
		refused.CredentialRequest = asking.CredentialRequest
		refused.LastError = reason
		socket, enabled, provided := connecting(t, asking, refused)

		var labels []string
		terminal := func(a *app) {
			a.prompt = func(_ context.Context, label string, secret bool) (string, error) {
				labels = append(labels, label)
				return "answer", nil
			}
		}
		shown := reason
		if shown == "" {
			shown = "credentials rejected"
		}
		r := runAt(t, socket, "", terminal, "connect", "home")
		if r.code != exitFailure || r.stderr != "home: "+shown+"\nhome: "+shown+"\nplaitway: home: "+shown+"\n" {
			t.Errorf("reason %q: connect: %+v", reason, r)
		}
		// The user name is asked once; the password until the limit.
		want := []string{"Username for home: ", "Password for home: ", "Password for home: ", "Password for home: "}
		if strings.Join(labels, "|") != strings.Join(want, "|") || len(provided()) != maxCredentialAttempts {
			t.Errorf("reason %q: asked %q, sent %d times", reason, labels, len(provided()))
		}
		// Nobody can answer any more, so the profile stops connecting.
		if got := enabled(); len(got) != 2 || !got[0] || got[1] {
			t.Errorf("reason %q: enable calls = %v, want connect then disconnect", reason, got)
		}
	}
}

func TestConnectWaitEnds(t *testing.T) {
	t.Parallel()
	connectingNow := profileOf("ID", "home", pb.ProfileState_PROFILE_STATE_CONNECTING)

	t.Run("timeout", func(t *testing.T) {
		t.Parallel()
		socket, enabled, _ := connecting(t, connectingNow)
		r := runAt(t, socket, "", nil, "connect", "-timeout", "300ms", "home")
		if r.code != exitFailure || r.stderr != "plaitway: home: still connecting after 300ms\n" {
			t.Errorf("connect: %+v", r)
		}
		// It keeps connecting: giving up waiting is not giving up connecting.
		if got := enabled(); len(got) != 1 || !got[0] {
			t.Errorf("enable calls = %v", got)
		}
	})

	t.Run("interrupt", func(t *testing.T) {
		t.Parallel()
		socket, enabled, _ := connecting(t, connectingNow)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		time.AfterFunc(300*time.Millisecond, cancel)
		r := runAtContext(t, ctx, socket, "", nil, "connect", "home")
		if r.code != exitFailure || r.stderr != "plaitway: interrupted\n" {
			t.Errorf("connect: %+v", r)
		}
		if got := enabled(); len(got) != 1 || !got[0] {
			t.Errorf("enable calls = %v", got)
		}
	})

	t.Run("disconnected by someone else", func(t *testing.T) {
		t.Parallel()
		socket, _, _ := connecting(t, profileOf("ID", "home", pb.ProfileState_PROFILE_STATE_DISCONNECTED))
		if r := runAt(t, socket, "", nil, "connect", "home"); r.code != exitFailure || r.stderr != "plaitway: home: disconnected before it was up\n" {
			t.Errorf("connect: %+v", r)
		}
	})
}

func TestStreamsThatEndAreExplained(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		watch func(grpc.ServerStreamingServer[pb.ProfileEvent]) error
		want  string
	}{
		"closed by the daemon": {func(grpc.ServerStreamingServer[pb.ProfileEvent]) error { return nil }, "plaitway: the daemon closed the stream\n"},
		"too slow": {func(grpc.ServerStreamingServer[pb.ProfileEvent]) error {
			return status.Error(codes.ResourceExhausted, "watcher too slow, dropped")
		}, "plaitway: watcher too slow, dropped (ResourceExhausted)\n"},
	} {
		socket := (&scripted{watch: c.watch}).serve(t)
		if r := runAt(t, socket, "", nil, "watch"); r.code != exitFailure || r.stderr != c.want {
			t.Errorf("%s: %+v", name, r)
		}
	}
}

func TestImportInlinesFilesNextToTheProfileOnly(t *testing.T) {
	t.Parallel()
	var (
		mu       sync.Mutex
		uploaded []*pb.ImportProfileRequest
	)
	socket := (&scripted{importFn: func(req *pb.ImportProfileRequest) (*pb.ImportProfileResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		uploaded = append(uploaded, req)
		return &pb.ImportProfileResponse{Profile: &pb.Profile{Id: "ID", Name: "office"}}, nil
	}}).serve(t)

	dir := t.TempDir()
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("profiles/office.ovpn", "client\nremote vpn.example.com\nca ca.crt\ncert certs/client.crt\nkey \"certs/client.key\"\ntls-auth ta.key 1\n")
	write("profiles/ca.crt", "-----BEGIN CERTIFICATE-----\nCA\n-----END CERTIFICATE-----\n")
	write("profiles/certs/client.crt", "-----BEGIN CERTIFICATE-----\nCLIENT\n-----END CERTIFICATE-----\n")
	write("profiles/certs/client.key", "-----BEGIN PRIVATE KEY-----\nKEY\n-----END PRIVATE KEY-----\n")
	write("profiles/ta.key", "-----BEGIN OpenVPN Static key V1-----\nTA\n-----END OpenVPN Static key V1-----\n")
	write("secret.pem", "-----BEGIN PRIVATE KEY-----\nOTHER\n-----END PRIVATE KEY-----\n")

	r := runAt(t, socket, "", nil, "import", filepath.Join(dir, "profiles", "office.ovpn"))
	if r.code != 0 || r.stdout != "imported office (id ID)\n" {
		t.Fatalf("import: %+v", r)
	}
	if len(uploaded) != 1 || uploaded[0].SourceFilename != "office.ovpn" {
		t.Fatalf("uploaded %v", uploaded)
	}
	want := "client\nremote vpn.example.com\n" +
		"<ca>\n-----BEGIN CERTIFICATE-----\nCA\n-----END CERTIFICATE-----\n</ca>\n" +
		"<cert>\n-----BEGIN CERTIFICATE-----\nCLIENT\n-----END CERTIFICATE-----\n</cert>\n" +
		"<key>\n-----BEGIN PRIVATE KEY-----\nKEY\n-----END PRIVATE KEY-----\n</key>\n" +
		"<tls-auth>\n-----BEGIN OpenVPN Static key V1-----\nTA\n-----END OpenVPN Static key V1-----\n</tls-auth>\nkey-direction 1\n"
	if got := string(uploaded[0].Content); got != want {
		t.Errorf("uploaded content:\n%s\nwant:\n%s", got, want)
	}

	// A profile that names a file elsewhere sends nothing.
	write("profiles/escape.ovpn", "client\nremote x\nkey ../secret.pem\n")
	write("profiles/absolute.ovpn", "client\nremote x\nkey "+filepath.Join(dir, "secret.pem")+"\n")
	for _, name := range []string{"escape", "absolute"} {
		r := runAt(t, socket, "", nil, "import", filepath.Join(dir, "profiles", name+".ovpn"))
		if r.code != exitFailure || !strings.Contains(r.stderr, "key ") || !strings.Contains(r.stderr, "is outside the directory of the profile") {
			t.Errorf("%s: %+v", name, r)
		}
	}
	if len(uploaded) != 1 {
		t.Errorf("a profile that names a file elsewhere was uploaded: %v", uploaded[1:])
	}
}

func TestImportRefusedByTheDaemon(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		err  error
		want string
	}{
		{status.Error(codes.PermissionDenied, "uid 501 is not an administrator, which /plaitway.v1.DaemonService/ImportProfile requires"),
			"plaitway: permission denied: uid 501 is not an administrator, which /plaitway.v1.DaemonService/ImportProfile requires\n"},
		{status.Error(codes.InvalidArgument, "line 3: ca refers to a file"), "plaitway: %s was rejected: line 3: ca refers to a file\n"},
	} {
		socket := (&scripted{importFn: func(*pb.ImportProfileRequest) (*pb.ImportProfileResponse, error) { return nil, c.err }}).serve(t)
		path := filepath.Join(t.TempDir(), "p.ovpn")
		if err := os.WriteFile(path, []byte(ovpnProfile), 0o600); err != nil {
			t.Fatal(err)
		}
		want := strings.ReplaceAll(c.want, "%s", path)
		if r := runAt(t, socket, "", nil, "import", path); r.code != exitFailure || r.stderr != want {
			t.Errorf("%v: %+v, want stderr %q", c.err, r, want)
		}
	}
}

// The menu bar app answers a credential request from its Keychain, usually
// before a person has typed at the terminal.
func TestConnectIsDoneWhenAnotherClientAnsweredFirst(t *testing.T) {
	t.Parallel()
	asking := profileOf("ID", "home", pb.ProfileState_PROFILE_STATE_AWAITING_CREDENTIALS)
	asking.CredentialRequest = &pb.CredentialRequest{Kind: pb.CredentialKind_CREDENTIAL_KIND_USER_PASSWORD}
	up := profileOf("ID", "home", pb.ProfileState_PROFILE_STATE_CONNECTED)

	var mu sync.Mutex
	var watches int
	socket := (&scripted{
		list: func() ([]*pb.Profile, error) { return []*pb.Profile{asking}, nil },
		enable: func(req *pb.SetProfileEnabledRequest) (*pb.Profile, error) {
			return profileOf(req.Id, "home", pb.ProfileState_PROFILE_STATE_CONNECTING), nil
		},
		provide: func(*pb.ProvideCredentialsRequest) (*pb.Profile, error) {
			return nil, status.Error(codes.FailedPrecondition, "profile is not awaiting credentials")
		},
		watch: func(stream grpc.ServerStreamingServer[pb.ProfileEvent]) error {
			mu.Lock()
			watches++
			state := asking
			if watches > 1 {
				state = up
			}
			mu.Unlock()
			if err := stream.Send(snapshot(state)); err != nil {
				return err
			}
			<-stream.Context().Done()
			return status.FromContextError(stream.Context().Err()).Err()
		},
	}).serve(t)

	if r := runAt(t, socket, "pw\n", nil, "connect", "-username", "alice", "home"); r.code != 0 || r.stdout != "home: connected\n" {
		t.Errorf("connect: %+v", r)
	}
}

// The daemon counts the lines of what it received, which has the files inline.
func TestImportReportsTheLinesOfTheFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"p.ovpn":     "client\nremote x\nca ca.crt\ncert client.crt\nup /bin/hook\n",
		"ca.crt":     pemCA + "\n",
		"client.crt": pemCert + "\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "p.ovpn")

	// In the content the daemon gets, "up" is line 13.
	var rejected atomic.Bool
	socket := (&scripted{importFn: func(req *pb.ImportProfileRequest) (*pb.ImportProfileResponse, error) {
		if lines := strings.Split(string(req.Content), "\n"); lines[12] != "up /bin/hook" {
			t.Errorf("line 13 of the content is %q", lines[12])
		}
		if rejected.Load() {
			return nil, status.Error(codes.InvalidArgument, "line 13: up is refused")
		}
		return &pb.ImportProfileResponse{
			Profile:  &pb.Profile{Id: "ID", Name: "p"},
			Warnings: []*pb.ImportWarning{{Line: 13, Directive: "up", Message: "removed"}, {Line: 0, Message: "no line"}},
		}, nil
	}}).serve(t)

	r := runAt(t, socket, "", nil, "import", path)
	if r.code != 0 || r.stderr != "warning: line 5: up: removed\nwarning: no line\n" {
		t.Errorf("import: %+v", r)
	}
	r = runAt(t, socket, "", nil, "import", "-json", path)
	var out struct{ Warnings []struct{ Line int } }
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil || len(out.Warnings) != 2 || out.Warnings[0].Line != 5 {
		t.Errorf("import -json: %q, %v", r.stdout, err)
	}

	rejected.Store(true)
	r = runAt(t, socket, "", nil, "import", path)
	if r.code != exitFailure || r.stderr != "plaitway: "+path+" was rejected: line 5: up is refused\n" {
		t.Errorf("import of a rejected profile: %+v", r)
	}
}
