package webresearch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// observedAlways serves tests whose subject is not address provenance.
func observedAlways(string, string) bool { return true }

// gateRecorder captures every dial the confinement hands to the approval gate.
type gateRecorder struct {
	mu    sync.Mutex
	dials []confine.EgressCommand
	ports []uint16
	allow bool
}

func recordGate(t *testing.T, allow bool) *gateRecorder {
	t.Helper()
	rec := &gateRecorder{allow: allow}
	confine.SetEgressResolver(func(_ context.Context, cmd confine.EgressCommand, ep egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		rec.dials = append(rec.dials, cmd)
		rec.ports = append(rec.ports, ep.Port)
		return rec.allow
	})
	t.Cleanup(func() { confine.SetEgressResolver(nil) })
	return rec
}

func (r *gateRecorder) reached() []confine.EgressCommand {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]confine.EgressCommand(nil), r.dials...)
}

// provenanceSite serves a page and a second origin that redirects to it.
func provenanceSite(t *testing.T) (page, redirector *httptest.Server) {
	t.Helper()
	page = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte("# Notes\n\nA page of notes long enough to read.\n"))
	}))
	t.Cleanup(page.Close)
	redirector = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, page.URL+"/landing"+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)
	return page, redirector
}

func fetchWithObserved(t *testing.T, observed map[string]string, tctx tools.ToolContext, rawURL string) error {
	t.Helper()
	reg := tools.NewDefaultRegistry()
	deps := ToolDeps{ObservedURL: func(sessionID, u string) bool { return observed[u] == sessionID }}
	testutil.FailErr(t, "RegisterToolsWithFactory", RegisterToolsWithFactory(reg, deps, nil))
	_, err := reg.Run(context.Background(), "fetch_url", map[string]any{"url": rawURL}, tctx)
	return err
}

func provenanceEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	allowLoopbackFetch(t)
}

// An address the chat observed is a source the reader's contract covers; an
// address the agent wrote is the agent's own dial and reaches the gate.
func TestFetchURLGatesOnlyAddressesTheAgentWrote(t *testing.T) {
	provenanceEnv(t)
	page, _ := provenanceSite(t)
	worker := tools.ToolContext{Identity: tools.InvocationIdentity{
		SessionID: "worker", RootSessionID: "chat", ToolCallID: "call",
	}}

	for name, tc := range map[string]struct {
		url      string
		observed map[string]string
		authored bool
	}{
		"observed by the calling session": {url: page.URL + "/own", observed: map[string]string{page.URL + "/own": "worker"}},
		"observed by the chat":            {url: page.URL + "/chat", observed: map[string]string{page.URL + "/chat": "chat"}},
		"observed by another chat":        {url: page.URL + "/other", observed: map[string]string{page.URL + "/other": "elsewhere"}, authored: true},
		"same host, different address":    {url: page.URL + "/seen?d=extra", observed: map[string]string{page.URL + "/seen": "chat"}, authored: true},
		"nothing observed":                {url: page.URL + "/new", authored: true},
	} {
		t.Run(name, func(t *testing.T) {
			gate := recordGate(t, true)
			testutil.FailErr(t, "fetch_url", fetchWithObserved(t, tc.observed, worker, tc.url))
			dials := gate.reached()
			if !tc.authored {
				if len(dials) != 0 {
					t.Fatalf("an observed address reached the gate %d times", len(dials))
				}
				return
			}
			if len(dials) != 1 || !dials[0].AuthoredDestination || confine.RetrievalDial(dials[0]) {
				t.Fatalf("an authored address must reach the gate once as the agent's own dial: %+v", dials)
			}
		})
	}
}

// Without an evidence reader nothing counts as observed.
func TestFetchURLWithoutEvidenceReaderIsAuthored(t *testing.T) {
	provenanceEnv(t)
	page, _ := provenanceSite(t)
	gate := recordGate(t, true)
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "RegisterToolsWithFactory", RegisterToolsWithFactory(reg, ToolDeps{}, nil))
	_, err := reg.Run(context.Background(), "fetch_url", map[string]any{"url": page.URL + "/unwired"},
		tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "chat", ToolCallID: "call"}})
	testutil.FailErr(t, "fetch_url", err)
	if dials := gate.reached(); len(dials) != 1 || !dials[0].AuthoredDestination {
		t.Fatalf("unwired evidence must gate the dial: %+v", dials)
	}
}

// Every hop carries the provenance of the address that started the request.
func TestFetchURLRedirectHopsInheritProvenance(t *testing.T) {
	provenanceEnv(t)
	_, redirector := provenanceSite(t)
	chat := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "chat", ToolCallID: "call"}}

	t.Run("authored", func(t *testing.T) {
		gate := recordGate(t, true)
		testutil.FailErr(t, "fetch_url", fetchWithObserved(t, nil, chat, redirector.URL+"/a"))
		dials := gate.reached()
		if len(dials) != 2 || gate.ports[0] == gate.ports[1] {
			t.Fatalf("both origins of an authored request must reach the gate: %d dials, ports %v", len(dials), gate.ports)
		}
		for _, dial := range dials {
			if !dial.AuthoredDestination {
				t.Fatalf("a redirect hop lost the request's provenance: %+v", dial)
			}
		}
	})

	t.Run("observed", func(t *testing.T) {
		gate := recordGate(t, true)
		start := redirector.URL + "/b"
		testutil.FailErr(t, "fetch_url", fetchWithObserved(t, map[string]string{start: "chat"}, chat, start))
		if dials := gate.reached(); len(dials) != 0 {
			t.Fatalf("an observed request's redirect reached the gate: %+v", dials)
		}
	})
}

// A refused authored address returns the host denial and fetches nothing.
func TestFetchURLAuthoredAddressDenied(t *testing.T) {
	provenanceEnv(t)
	served := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served++
		_, _ = w.Write([]byte("body"))
	}))
	t.Cleanup(srv.Close)
	recordGate(t, false)
	err := fetchWithObserved(t, nil,
		tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "chat", ToolCallID: "call"}}, srv.URL+"/refused")
	reject := &toolrejection.ToolReject{}
	if !errors.As(err, &reject) || reject.Code != "WEB_SEARCH_HOST_DENIED" {
		t.Fatalf("error = %v, want WEB_SEARCH_HOST_DENIED", err)
	}
	if served != 0 {
		t.Fatalf("a refused request reached the server %d times", served)
	}
}
