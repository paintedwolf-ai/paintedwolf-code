package confine

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/confine/internal/admissionstate"
	"github.com/lycaon/lycaon/internal/egressproxy"
)

func TestRequireAppliedHonorsExplicitSandboxPolicy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		auto     bool
		sandbox  string
		bypass   string
		applied  bool
		wantStop bool
	}{
		{name: "required without boundary", auto: true, wantStop: true},
		{name: "applied", auto: true, applied: true},
		{name: "explicit sandbox off", auto: true, sandbox: "off"},
		{name: "explicit bypass", auto: true, bypass: "1"},
		{name: "not enabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := admissionstate.ProcessEnabled.Swap(tc.auto)
			t.Cleanup(func() { admissionstate.ProcessEnabled.Store(previous) })
			t.Setenv("LYCAON_SANDBOX", tc.sandbox)
			t.Setenv("LYCAON_BYPASS_APPROVALS", tc.bypass)
			err := RequireApplied(tc.applied)
			if errors.Is(err, ErrNotConfined) != tc.wantStop || (err != nil && !tc.wantStop) {
				t.Fatalf("RequireApplied(%v) = %v, want refusal %v", tc.applied, err, tc.wantStop)
			}
		})
	}
}

type countingHandler struct{ records *atomic.Int32 }

func (h countingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h countingHandler) Handle(context.Context, slog.Record) error {
	h.records.Add(1)
	return nil
}
func (h countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h countingHandler) WithGroup(string) slog.Handler      { return h }

// A retrying unleased process is warned about once per destination.
func TestOrphanRefusalWarnsOncePerDestination(t *testing.T) {
	var records atomic.Int32
	prev := slog.Default()
	slog.SetDefault(slog.New(countingHandler{&records}))
	t.Cleanup(func() { slog.SetDefault(prev) })

	broker := &egressBrokerT{}
	peer := egressproxy.Peer{Lineage: "orphan-warn-once", Owner: "./build --all"}
	first := egressproxy.Endpoint{Host: "registry.example.test", Port: 443, Transport: egressproxy.TransportHTTPConnect}
	second := egressproxy.Endpoint{Host: "mirror.example.test", Port: 443, Transport: egressproxy.TransportHTTPConnect}
	for range 3 {
		broker.reportRefusal(peer, first, egressproxy.Refusal("unleased"))
	}
	broker.reportRefusal(peer, second, egressproxy.Refusal("unleased"))
	if got := records.Load(); got != 2 {
		t.Fatalf("warnings = %d, want one per destination", got)
	}
}
