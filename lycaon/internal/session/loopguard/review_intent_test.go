package loopguard

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestReviewIntentRenewsAllRepetitionCounters(t *testing.T) {
	g := NewMemoryDoomLoopGuard()
	intent := "first"
	g.SetReviewIntentResolver(func(context.Context, string) (string, error) { return intent, nil })
	args := map[string]any{"command": "read protected-file"}
	check := func(response string) bool {
		t.Helper()
		allowed, _, _, err := g.Check(t.Context(), "worker", response, "command", args)
		if err != nil {
			t.Fatal(err)
		}
		return allowed
	}
	if !check("first") {
		t.Fatal("initial request blocked")
	}
	for i := 0; i < DoomLoopMaxSameCodeRejects; i++ {
		if !check(fmt.Sprint(i)) {
			t.Fatal("attempt blocked before threshold")
		}
		if err := g.RecordAttempt(t.Context(), "worker", fmt.Sprint(i), "command", args, "SANDBOX_READ_PATH_DENIED", false); err != nil {
			t.Fatal(err)
		}
	}
	if check("blocked") {
		t.Fatal("unchanged intent must remain blocked")
	}
	if _, err := g.RecordSearchOutcome(t.Context(), "worker", "search", map[string]any{"query": "missing"}, false); err != nil {
		t.Fatal(err)
	}
	intent = "fresh direction"
	if !check("renewed") {
		t.Fatal("fresh direction remained blocked")
	}
	if got := g.CodeRejectResponses("worker", "command", "SANDBOX_READ_PATH_DENIED"); got != 0 {
		t.Fatalf("rejection count = %d", got)
	}
	if got := g.FruitlessSearchRun("worker", "search", map[string]any{"query": "missing"}); got != 0 {
		t.Fatalf("search count = %d", got)
	}
}

func TestReviewIntentReadFailureStopsCheck(t *testing.T) {
	g := NewMemoryDoomLoopGuard()
	failure := errors.New("intent unavailable")
	g.SetReviewIntentResolver(func(context.Context, string) (string, error) { return "", failure })
	allowed, _, _, err := g.Check(t.Context(), "worker", "response", "command", nil)
	if err := g.RecordAttempt(t.Context(), "worker", "response", "command", nil, "DENIED", false); !errors.Is(err, failure) {
		t.Fatalf("record error=%v", err)
	}
	if allowed || !errors.Is(err, failure) {
		t.Fatalf("allowed=%v error=%v", allowed, err)
	}
}

func TestLateResponseCannotRestoreRepetitionAfterDirection(t *testing.T) {
	g := NewMemoryDoomLoopGuard()
	intent := "old"
	g.SetReviewIntentResolver(func(context.Context, string) (string, error) { return intent, nil })
	args := map[string]any{"command": "read protected-file"}
	for i := 0; i < DoomLoopMaxCodeRepeats; i++ {
		_, _, _, err := g.Check(t.Context(), "worker", fmt.Sprint(i), "command", args)
		if err != nil {
			t.Fatal(err)
		}
	}
	intent = "direction attached to rejection"
	for i := 0; i < DoomLoopMaxCodeRepeats; i++ {
		if err := g.RecordAttempt(t.Context(), "worker", fmt.Sprint(i), "command", args, "SANDBOX_READ_PATH_DENIED", false); err != nil {
			t.Fatal(err)
		}
	}
	if got := g.CodeRejectResponses("worker", "command", "SANDBOX_READ_PATH_DENIED"); got != 0 {
		t.Fatalf("late rejections restored count %d", got)
	}
	allowed, _, _, err := g.Check(t.Context(), "worker", "renewed", "command", args)
	if err != nil || !allowed {
		t.Fatalf("renewed allowed=%v, error=%v", allowed, err)
	}
}
