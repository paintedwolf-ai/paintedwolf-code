package webresearch

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func periodToolRegistry(t *testing.T, fake *FakeDirectDiscoverer) *tools.DefaultRegistry {
	t.Helper()
	reg := tools.NewDefaultRegistry()
	factory := func(_ context.Context, _ tools.ToolContext) DirectDiscoverer { return fake }
	err := RegisterToolsWithFactory(reg, ToolDeps{}, func() DirectDiscovererFactory { return factory })
	testutil.FailErr(t, "RegisterToolsWithFactory", err)
	return reg
}

func runWebSearch(t *testing.T, reg *tools.DefaultRegistry, args map[string]any) (string, error) {
	t.Helper()
	return reg.Run(context.Background(), "web_search", args, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess-period"},
	})
}

func fakeHits() []WebHit {
	return []WebHit{{Title: "Doc", URL: "https://example.com/doc", Snippet: "guide", Provider: "direct"}}
}

func TestWebSearchDefaultsToCurrentWindow(t *testing.T) {
	fake := &FakeDirectDiscoverer{Hits: fakeHits()}
	out, err := runWebSearch(t, periodToolRegistry(t, fake), map[string]any{"query": "widget guide"})
	testutil.FailErr(t, "reg.Run", err)
	if !fake.LastPeriod.IsCurrent() {
		t.Fatalf("period = %q, want current", fake.LastPeriod.String())
	}
	if !strings.Contains(out, `"period": "current"`) {
		t.Fatalf("result must state the window it answered:\n%s", out)
	}
}

func TestWebSearchCarriesDeclaredWindowToThePipeline(t *testing.T) {
	fake := &FakeDirectDiscoverer{Hits: fakeHits()}
	out, err := runWebSearch(t, periodToolRegistry(t, fake),
		map[string]any{"query": "payment outage postmortem", "period": "2024-2025"})
	testutil.FailErr(t, "reg.Run", err)
	if got := fake.LastPeriod.String(); got != "2024-2025" {
		t.Fatalf("period reaching the pipeline = %q, want 2024-2025", got)
	}
	if !strings.Contains(out, `"period": "2024-2025"`) {
		t.Fatalf("result must echo the declared window:\n%s", out)
	}
}

func TestWebSearchRejectsUnreadableWindow(t *testing.T) {
	fake := &FakeDirectDiscoverer{Hits: fakeHits()}
	_, err := runWebSearch(t, periodToolRegistry(t, fake),
		map[string]any{"query": "widget guide", "period": "last year"})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) {
		t.Fatalf("want ToolReject, got %T %v", err, err)
	}
	if reject.Code != webSearchPeriodInvalidCode || reject.Observation != webSearchPeriodInvalidObservation {
		t.Fatalf("reject = %+v", reject)
	}
	if fake.Calls != 0 {
		t.Fatal("an unreadable window must not reach the pipeline")
	}
}

// A bare past year with no declared window teaches; the results still stand.
func TestWebSearchMarksBareYearForTeaching(t *testing.T) {
	fake := &FakeDirectDiscoverer{Hits: fakeHits()}
	out, err := runWebSearch(t, periodToolRegistry(t, fake),
		map[string]any{"query": "enterprise design trends 2025"})
	testutil.FailErr(t, "reg.Run", err)
	if !strings.Contains(out, guidance.MarkerPeriodHint) {
		t.Fatalf("want a period-hint marker on a bare-year query:\n%s", out)
	}
	if !strings.Contains(out, "https://example.com/doc") {
		t.Fatal("the search must still return its results")
	}
	if year, ok := guidance.ParsePeriodHintMarker(lastLine(out)); !ok || year != "2025" {
		t.Fatalf("marker year = %q ok=%v, want 2025", year, ok)
	}
}

func TestWebSearchDoesNotMarkSubjectYearsOrDeclaredWindows(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
	}{
		{"subject year", map[string]any{"query": "CVE-2025-1234 mitigation"}},
		{"declared window", map[string]any{"query": "payment outage 2025", "period": "2025"}},
		{"no year", map[string]any{"query": "react hooks guide"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &FakeDirectDiscoverer{Hits: fakeHits()}
			out, err := runWebSearch(t, periodToolRegistry(t, fake), tc.args)
			testutil.FailErr(t, "reg.Run", err)
			if strings.Contains(out, guidance.MarkerPeriodHint) {
				t.Fatalf("unexpected period hint:\n%s", out)
			}
		})
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}
