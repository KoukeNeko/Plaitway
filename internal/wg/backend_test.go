package wg

import (
	"strings"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

func TestBackendIsWireGuard(t *testing.T) {
	b := Backend(Config{})
	if b.Kind != tunnel.KindWireGuard || b.Parse == nil || b.New == nil || b.Probe == nil {
		t.Fatalf("Backend = %+v", b)
	}
}

func TestBackendNewRejectsABadProfile(t *testing.T) {
	b := Backend(Config{})
	spec := tunnel.Spec{Owner: "x", Name: "home", Content: []byte("[Interface]\nPostUp = true\n")}
	_, err := b.New(spec, tunnel.Deps{})
	if err == nil || !strings.Contains(err.Error(), "PostUp is not allowed") {
		t.Fatalf("New() error = %v, want the profile rejected", err)
	}
}
