package webresearch

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestWebSearchFiresSearchWarmHook(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	var fired atomic.Bool
	var gotQuery, gotSession string
	deps := ToolDeps{
		SearchWarmHook: func(_ context.Context, sessionID, _, query, _ string, hitURLs, _ []string, _, _ int, _ bool) {
			fired.Store(true)
			gotSession = sessionID
			gotQuery = query
			if len(hitURLs) != 1 || hitURLs[0] != "https://example.com/doc" {
				t.Fatalf("hitURLs = %v", hitURLs)
			}
		},
	}
	factory := func(_ context.Context, _ tools.ToolContext) DirectDiscoverer {
		return &FakeDirectDiscoverer{Hits: []WebHit{{
			Title: "Doc", URL: "https://example.com/doc", Snippet: "guide", Provider: "direct",
		}}}
	}
	if err := RegisterToolsWithFactory(reg, deps, func() DirectDiscovererFactory { return factory }); err != nil {
		testutil.FailErr(t, "RegisterToolsWithFactory failed", err)
	}
	out, err := reg.Run(context.Background(), "web_search", map[string]any{"query": "widget guide"}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess-1"},
	})
	testutil.FailErr(t, "reg.Run failed", err)
	if out == "" {
		t.Fatal("want tool output")
	}
	if !fired.Load() {
		t.Fatal("SearchWarmHook not called")
	}
	if gotQuery != "widget guide" || gotSession != "sess-1" {
		t.Fatalf("query=%q session=%q", gotQuery, gotSession)
	}
}

func TestWebSearchDisabledWhenSettingsOff(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	cfg := NewConfigStoreAt(filepath.Join(t.TempDir(), "web-research-config.yaml"))
	disabled := false
	testutil.FailErr(t, "ApplyPrefs", cfg.ApplyPrefs(nil, nil, &disabled, nil))
	deps := ToolDeps{Config: cfg}
	if err := RegisterToolsWithFactory(reg, deps, nil); err != nil {
		testutil.FailErr(t, "RegisterToolsWithFactory failed", err)
	}
	_, err := reg.Run(context.Background(), "web_search", map[string]any{"query": "blocked"}, tools.ToolContext{})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) {
		t.Fatalf("want ToolReject, got %T %v", err, err)
	}
	if reject.Code != webSearchDisabledCode || reject.Observation != webSearchDisabledObservation {
		t.Fatalf("reject = %+v", reject)
	}
}

func TestWebSearchFiresWarmHookOnEmptyDirectResults(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	var fired atomic.Bool
	var gotStrong, gotMax int
	var gotDirect bool
	deps := ToolDeps{
		SearchWarmHook: func(_ context.Context, _, _, _, _ string, _, _ []string, strongHits, maxResults int, directParticipated bool) {
			fired.Store(true)
			gotStrong = strongHits
			gotMax = maxResults
			gotDirect = directParticipated
		},
	}
	factory := func(_ context.Context, _ tools.ToolContext) DirectDiscoverer {
		return &FakeDirectDiscoverer{Hits: nil}
	}
	if err := RegisterToolsWithFactory(reg, deps, func() DirectDiscovererFactory { return factory }); err != nil {
		testutil.FailErr(t, "RegisterToolsWithFactory failed", err)
	}
	_, err := reg.Run(context.Background(), "web_search", map[string]any{"query": "nothing", "limit": 5}, tools.ToolContext{})
	testutil.FailErr(t, "reg.Run failed", err)
	if !fired.Load() {
		t.Fatal("SearchWarmHook must fire on OK empty Direct underfilled")
	}
	if !gotDirect || gotStrong != 0 || gotMax != 5 {
		t.Fatalf("strong=%d max=%d direct=%v", gotStrong, gotMax, gotDirect)
	}
}

func TestWebSearchRejectsInvalidQueryWithoutWarmHook(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	var fired atomic.Bool
	deps := ToolDeps{
		SearchWarmHook: func(context.Context, string, string, string, string, []string, []string, int, int, bool) {
			fired.Store(true)
		},
	}
	if err := RegisterToolsWithFactory(reg, deps, nil); err != nil {
		testutil.FailErr(t, "RegisterToolsWithFactory failed", err)
	}
	_, err := reg.Run(context.Background(), "web_search", map[string]any{"query": ""}, tools.ToolContext{})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) {
		t.Fatalf("want ToolReject, got %T %v", err, err)
	}
	if reject.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("reject = %+v", reject)
	}
	if fired.Load() {
		t.Fatal("SearchWarmHook must not run for an invalid query")
	}
}

func TestWebSearchRejectsWhenEveryProviderFails(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	var fired atomic.Bool
	deps := ToolDeps{
		SearchWarmHook: func(context.Context, string, string, string, string, []string, []string, int, int, bool) {
			fired.Store(true)
		},
	}
	factory := func(context.Context, tools.ToolContext) DirectDiscoverer {
		return &FakeDirectDiscoverer{Err: fmt.Errorf("upstream unavailable")}
	}
	if err := RegisterToolsWithFactory(reg, deps, func() DirectDiscovererFactory { return factory }); err != nil {
		testutil.FailErr(t, "register web tools", err)
	}
	_, err := reg.Run(context.Background(), "web_search", map[string]any{"query": "current widget guide"}, tools.ToolContext{})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) {
		t.Fatalf("want ToolReject, got %T %v", err, err)
	}
	if reject.Code != webSearchProvidersFailedCode || reject.Observation != webSearchProvidersFailedObservation {
		t.Fatalf("reject = %+v", reject)
	}
	if fired.Load() {
		t.Fatal("SearchWarmHook must not run after provider failure")
	}
}

// TestFetchURLFiresFetchWarmHookOnFreshFetchOnly: a fresh fetch fires the
// post-fetch warm hook with the page URL and title; a cache-hit re-fetch
// stays silent.
func TestFetchURLFiresFetchWarmHookOnFreshFetchOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	allowLoopbackFetch(t)
	confine.SetEgressResolver(func(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
		return true
	})
	t.Cleanup(func() { confine.SetEgressResolver(nil) })
	srv := warmTestSite(t)

	reg := tools.NewDefaultRegistry()
	var calls atomic.Int64
	var gotURL, gotTitle string
	deps := ToolDeps{
		FetchWarmHook: func(_ context.Context, _, _, pageURL, title, _ string) {
			calls.Add(1)
			gotURL = pageURL
			gotTitle = title
		},
	}
	if err := RegisterToolsWithFactory(reg, deps, nil); err != nil {
		testutil.FailErr(t, "RegisterToolsWithFactory failed", err)
	}
	args := map[string]any{"url": srv.URL + "/guide.md"}
	if _, err := reg.Run(context.Background(), "fetch_url", args, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess-f"},
	}); err != nil {
		testutil.FailErr(t, "reg.Run failed", err)
	}
	if calls.Load() != 1 || gotURL != srv.URL+"/guide.md" || gotTitle != "Widget frobnicator guide" {
		t.Fatalf("calls=%d url=%q title=%q", calls.Load(), gotURL, gotTitle)
	}
	// Cache hit: no network fetch, no warm.
	if _, err := reg.Run(context.Background(), "fetch_url", args, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess-f"},
	}); err != nil {
		testutil.FailErr(t, "reg.Run failed", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d want no hook on cache hit", calls.Load())
	}
}
