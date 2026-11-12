package cost

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSessionUsageSinceSumsOnlyTheWindow(t *testing.T) {
	tracker, _ := newTestTracker(t, nil)
	start := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	record := func(id, session string, at time.Time, prompt, completion int) {
		t.Helper()
		nano := int64(prompt+completion) * 1000
		event := UsageEvent{
			ID: id, SessionID: session, ProjectID: "project-1", ProviderID: "mock", Model: "m",
			Caller: CallerWorker, StartedAt: at, PromptTokens: prompt, CompletionTokens: completion,
			EstimatedNanoUSD: &nano,
		}
		testutil.FailErr(t, "begin call", tracker.BeginCall(t.Context(), event))
		testutil.FailErr(t, "record usage", tracker.RecordUsage(t.Context(), event))
	}
	record("before", "child", start.Add(-time.Minute), 1000, 1000)
	record("first", "child", start, 100, 20)
	record("second", "child", start.Add(time.Minute), 200, 30)
	record("other", "sibling", start.Add(time.Minute), 5000, 5000)

	got, err := tracker.SessionUsageSince(t.Context(), "child", start)
	testutil.FailErr(t, "session usage", err)
	if got.Calls != 2 || got.Tokens != 350 || !got.Priced || got.SpendNanoUSD != 350_000 {
		t.Fatalf("usage = %+v, want 2 priced calls and 350 tokens", got)
	}

	empty, err := tracker.SessionUsageSince(t.Context(), "child", start.Add(time.Hour))
	testutil.FailErr(t, "empty window", err)
	if empty.Calls != 0 || empty.Tokens != 0 || !empty.Priced {
		t.Fatalf("empty window = %+v", empty)
	}
	if _, err := tracker.SessionUsageSince(t.Context(), " ", start); err == nil {
		t.Fatal("blank session id accepted")
	}
}

func TestSessionUsageSinceFlagsUnpricedCalls(t *testing.T) {
	tracker, _ := newTestTracker(t, nil)
	start := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	event := UsageEvent{ID: "unpriced", SessionID: "child", Caller: CallerWorker, StartedAt: start, PromptTokens: 10, CompletionTokens: 5}
	testutil.FailErr(t, "begin call", tracker.BeginCall(t.Context(), event))
	testutil.FailErr(t, "record usage", tracker.RecordUsage(t.Context(), event))
	got, err := tracker.SessionUsageSince(t.Context(), "child", start)
	testutil.FailErr(t, "session usage", err)
	if got.Calls != 1 || got.Priced {
		t.Fatalf("usage = %+v, want one unpriced call", got)
	}
}
