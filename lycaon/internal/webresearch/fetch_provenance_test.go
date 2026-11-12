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

// sourceAlways serves tests whose subject is not address provenance.
func sourceAlways(string, string) confine.AddressOrigin { return confine.AddressSource }

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

// known is what each session's evidence says about an address.
type known map[string]map[string]confine.AddressOrigin

func (k known) origin(sessionID, rawURL string) confine.AddressOrigin {
	if origin, ok := k[sessionID][rawURL]; ok {
		return origin
	}
	return confine.AddressAuthored
}

func fetchKnowing(t *testing.T, evidence known, tctx tools.ToolContext, rawURL string) error {
	t.Helper()
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "RegisterToolsWithFactory", RegisterToolsWithFactory(reg, ToolDeps{AddressOrigin: evidence.origin}, nil))
	_, err := reg.Run(context.Background(), "fetch_url", map[string]any{"url": rawURL}, tctx)
	return err
}

func provenanceEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	allowLoopbackFetch(t)
}

// A source stays with the reader's contract, and any other address reaches
// the gate once carrying how the chat came by it. The calling session and its
// chat both count, the stronger winning.
func TestFetchURLStatesHowTheChatCameByTheAddress(t *testing.T) {
	provenanceEnv(t)
	page, _ := provenanceSite(t)
	worker := tools.ToolContext{Identity: tools.InvocationIdentity{
		SessionID: "worker", RootSessionID: "chat", ToolCallID: "call",
	}}
	address := page.URL + "/doc"
	const source, displayed, authored = confine.AddressSource, confine.AddressDisplayed, confine.AddressAuthored

	for name, tc := range map[string]struct {
		evidence known
		want     confine.AddressOrigin
	}{
		"source for the calling session": {evidence: known{"worker": {address: source}}, want: source},
		"source for the chat":            {evidence: known{"chat": {address: source}}, want: source},
		"displayed to the session":       {evidence: known{"worker": {address: displayed}}, want: displayed},
		"displayed to the chat":          {evidence: known{"chat": {address: displayed}}, want: displayed},
		"displayed here, source there":   {evidence: known{"worker": {address: displayed}, "chat": {address: source}}, want: source},
		"known to another chat":          {evidence: known{"elsewhere": {address: source}}, want: authored},
		"same host, different address":   {evidence: known{"chat": {page.URL + "/doc?d=extra": source}}, want: authored},
		"nothing known":                  {want: authored},
	} {
		t.Run(name, func(t *testing.T) {
			gate := recordGate(t, true)
			testutil.FailErr(t, "fetch_url", fetchKnowing(t, tc.evidence, worker, address))
			dials := gate.reached()
			if tc.want == source {
				if len(dials) != 0 {
					t.Fatalf("a source reached the gate %d times", len(dials))
				}
				return
			}
			if len(dials) != 1 || dials[0].Origin != tc.want || confine.RetrievalDial(dials[0]) {
				t.Fatalf("want one dial at the gate with origin %d: %+v", tc.want, dials)
			}
		})
	}
}

// Without an evidence reader the agent wrote every address.
func TestFetchURLWithoutEvidenceReaderIsAuthored(t *testing.T) {
	provenanceEnv(t)
	page, _ := provenanceSite(t)
	gate := recordGate(t, true)
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "RegisterToolsWithFactory", RegisterToolsWithFactory(reg, ToolDeps{}, nil))
	_, err := reg.Run(context.Background(), "fetch_url", map[string]any{"url": page.URL + "/unwired"},
		tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "chat", ToolCallID: "call"}})
	testutil.FailErr(t, "fetch_url", err)
	if dials := gate.reached(); len(dials) != 1 || dials[0].Origin != confine.AddressAuthored {
		t.Fatalf("unwired evidence must gate the dial: %+v", dials)
	}
}

// Every hop carries the origin of the address that started the request.
func TestFetchURLRedirectHopsInheritTheOrigin(t *testing.T) {
	provenanceEnv(t)
	_, redirector := provenanceSite(t)
	chat := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "chat", ToolCallID: "call"}}

	for name, origin := range map[string]confine.AddressOrigin{
		"authored": confine.AddressAuthored, "displayed": confine.AddressDisplayed,
	} {
		t.Run(name, func(t *testing.T) {
			gate := recordGate(t, true)
			start := redirector.URL + "/" + name
			testutil.FailErr(t, "fetch_url", fetchKnowing(t, known{"chat": {start: origin}}, chat, start))
			dials := gate.reached()
			if len(dials) != 2 || gate.ports[0] == gate.ports[1] {
				t.Fatalf("both origins of the request must reach the gate: %d dials, ports %v", len(dials), gate.ports)
			}
			for _, dial := range dials {
				if dial.Origin != origin {
					t.Fatalf("a redirect hop lost the request's origin: %+v", dial)
				}
			}
		})
	}

	t.Run("source", func(t *testing.T) {
		gate := recordGate(t, true)
		start := redirector.URL + "/source"
		testutil.FailErr(t, "fetch_url", fetchKnowing(t, known{"chat": {start: confine.AddressSource}}, chat, start))
		if dials := gate.reached(); len(dials) != 0 {
			t.Fatalf("a source's redirect reached the gate: %+v", dials)
		}
	})
}

// A refused address returns the host denial and fetches nothing.
func TestFetchURLRefusedAddressFetchesNothing(t *testing.T) {
	provenanceEnv(t)
	served := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served++
		_, _ = w.Write([]byte("body"))
	}))
	t.Cleanup(srv.Close)
	recordGate(t, false)
	err := fetchKnowing(t, nil,
		tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "chat", ToolCallID: "call"}}, srv.URL+"/refused")
	reject := &toolrejection.ToolReject{}
	if !errors.As(err, &reject) || reject.Code != "WEB_SEARCH_HOST_DENIED" {
		t.Fatalf("error = %v, want WEB_SEARCH_HOST_DENIED", err)
	}
	if served != 0 {
		t.Fatalf("a refused request reached the server %d times", served)
	}
}
