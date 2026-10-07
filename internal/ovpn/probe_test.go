package ovpn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProbeBinary(t *testing.T) {
	dir := t.TempDir()
	const (
		line27 = "OpenVPN 2.7.7 aarch64-apple-darwin27.0.0 [SSL (OpenSSL)] [LZO] [LZ4] [MH/RECVDA] [AEAD]"
		line26 = "OpenVPN 2.6.12 x86_64-apple-darwin23.0.0 [SSL (OpenSSL)] [LZ4] [MH/RECVDA] [AEAD]"
		line25 = "OpenVPN 2.5.9 x86_64-pc-linux-gnu [SSL (OpenSSL)] [LZO] [LZ4] [EPOLL] [MH/PKTINFO] [AEAD]"
	)
	// openvpn --version prints and exits with status 1.
	versionScript := func(first string) string {
		return "echo '" + first + "'\necho 'library versions: OpenSSL 3.5.9, LZO 2.10'\nexit 1\n"
	}
	tests := []struct {
		name string
		path string
		want binaryInfo
	}{
		{
			name: "2.7 with LZO",
			path: writeScript(t, dir, "v27", versionScript(line27)),
			want: binaryInfo{available: true, version: "2.7.7", major: 2, minor: 7, lzo: true},
		},
		{
			name: "2.6 without LZO",
			path: writeScript(t, dir, "v26", versionScript(line26)),
			want: binaryInfo{available: true, version: "2.6.12", major: 2, minor: 6},
		},
		{
			name: "2.5",
			path: writeScript(t, dir, "v25", versionScript(line25)),
			want: binaryInfo{available: true, version: "2.5.9", major: 2, minor: 5, lzo: true},
		},
		{
			name: "exit status 0 is fine too",
			path: writeScript(t, dir, "zero", "echo '"+line27+"'\n"),
			want: binaryInfo{available: true, version: "2.7.7", major: 2, minor: 7, lzo: true},
		},
		{
			name: "release candidate suffix",
			path: writeScript(t, dir, "rc", "echo 'OpenVPN 2.8.0_rc1 x [LZO]'\nexit 1\n"),
			want: binaryInfo{available: true, version: "2.8.0", major: 2, minor: 8, lzo: true},
		},
		{
			name: "not openvpn",
			path: writeScript(t, dir, "other", "echo 'GNU bash'\nexit 1\n"),
			want: binaryInfo{detail: " --version did not print an OpenVPN version"},
		},
		{
			name: "prints nothing",
			path: writeScript(t, dir, "silent", "exit 0\n"),
			want: binaryInfo{detail: " --version did not print an OpenVPN version"},
		},
		{
			name: "missing",
			path: filepath.Join(dir, "missing"),
			want: binaryInfo{detail: "openvpn not found at " + filepath.Join(dir, "missing")},
		},
		{
			name: "not executable",
			path: func() string {
				p := filepath.Join(dir, "plain")
				os.WriteFile(p, []byte("x"), 0o644)
				return p
			}(),
			want: binaryInfo{detail: "openvpn at " + filepath.Join(dir, "plain") + " cannot be executed"},
		},
		{
			name: "not configured",
			path: "",
			want: binaryInfo{detail: "openvpn binary is not configured"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := probeBinary(tt.path, 5*time.Second)
			if tt.want.available {
				if got != tt.want {
					t.Errorf("probeBinary = %+v, want %+v", got, tt.want)
				}
				return
			}
			if got.available {
				t.Fatalf("probeBinary = %+v, want unavailable", got)
			}
			// The detail names the path, so match with it filled in.
			want := strings.Replace(tt.want.detail, " --version", tt.path+" --version", 1)
			if got.detail != want {
				t.Errorf("detail = %q, want %q", got.detail, want)
			}
		})
	}
}

func TestProbeBinaryTimesOut(t *testing.T) {
	path := writeScript(t, t.TempDir(), "hang", "sleep 30\n")
	start := time.Now()
	got := probeBinary(path, 200*time.Millisecond)
	if got.available || !strings.Contains(got.detail, "did not finish") {
		t.Fatalf("probeBinary = %+v, want a timeout detail", got)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("the probe waited %v for a hung binary", time.Since(start))
	}
}

func TestBinaryInfoCapabilities(t *testing.T) {
	tests := []struct {
		major, minor    int
		disableDCO      bool
		persistKeyDeprc bool
	}{
		{2, 4, false, false}, {2, 5, false, false}, {2, 6, true, false}, {2, 7, true, true}, {3, 0, true, true},
	}
	for _, tt := range tests {
		i := binaryInfo{major: tt.major, minor: tt.minor}
		if i.supportsDisableDCO() != tt.disableDCO || i.persistKeyIsDeprecated() != tt.persistKeyDeprc {
			t.Errorf("%d.%d: disable-dco %v, persist-key deprecated %v", tt.major, tt.minor, i.supportsDisableDCO(), i.persistKeyIsDeprecated())
		}
	}
}

func TestBackendProbeReportsAndRunsOnce(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "count")
	path := writeScript(t, dir, "ovpn", "echo run >> '"+counter+"'\necho 'OpenVPN 2.7.7 x [LZO]'\nexit 1\n")
	b := Backend(Config{Binary: path, RunDir: t.TempDir()})
	if b.Kind != tunnel.KindOpenVPN {
		t.Errorf("Kind = %v", b.Kind)
	}
	for i := 0; i < 3; i++ {
		if info := b.Probe(); !info.Available || info.Version != "2.7.7" || info.Detail != "" {
			t.Fatalf("Probe = %+v", info)
		}
	}
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "run"); n != 1 {
		t.Errorf("openvpn --version ran %d times, want once", n)
	}
}

func TestBackendProbeWithoutLZOSaysSo(t *testing.T) {
	path := writeScript(t, t.TempDir(), "ovpn", "echo 'OpenVPN 2.6.12 x [LZ4]'\nexit 1\n")
	info := Backend(Config{Binary: path, RunDir: t.TempDir()}).Probe()
	if !info.Available || info.Version != "2.6.12 (no LZO)" {
		t.Errorf("Probe = %+v", info)
	}
}

func TestBackendProbeMissingBinary(t *testing.T) {
	info := Backend(Config{Binary: "/nonexistent/openvpn", RunDir: t.TempDir()}).Probe()
	if info.Available || info.Detail != "openvpn not found at /nonexistent/openvpn" || info.Version != "" {
		t.Errorf("Probe = %+v", info)
	}
}

// A probe that fails is not the daemon's last word: the binary may have been in
// the middle of an update, and a good answer is kept for good.
func TestBackendProbeLooksAgainWhileTheBinaryIsUnusable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "openvpn")
	b := newBackend(Config{Binary: path, RunDir: t.TempDir()})
	b.reprobe = 500 * time.Millisecond

	if b.info().available {
		t.Fatal("a missing binary is available")
	}
	writeScript(t, dir, "openvpn", "echo 'OpenVPN 2.7.7 x [LZO]'\nexit 1\n")
	if b.info().available {
		t.Error("the unusable answer was not remembered at all")
	}
	time.Sleep(600 * time.Millisecond)
	if got := b.info(); !got.available || got.version != "2.7.7" {
		t.Fatalf("the binary was not looked at again: %+v", got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond)
	if !b.info().available {
		t.Error("a usable answer was dropped")
	}
}
