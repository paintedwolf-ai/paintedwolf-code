package loopguard

import (
	"fmt"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRejectedBatchCountsOneResponse(t *testing.T) {
	g := NewMemoryDoomLoopGuard()
	for round := 1; round <= 3; round++ {
		response := fmt.Sprint(round)
		var workers sync.WaitGroup
		for file := range 8 {
			workers.Add(1)
			go func() {
				defer workers.Done()
				args := map[string]any{"path": fmt.Sprintf("portrait-%d.svg", file)}
				if err := g.RecordAttempt(t.Context(), "s", response, "view_image", args, "TOOL_NOT_OFFERED", false); err != nil {
					t.Errorf("record rejection: %v", err)
				}
			}()
		}
		workers.Wait()
		if got := g.CodeRejectResponses("s", "view_image", "TOOL_NOT_OFFERED"); got != round {
			t.Fatalf("round %d: rejected responses = %d", round, got)
		}
	}
}

func TestIdenticalBatchUsesPreviousResponseFeedback(t *testing.T) {
	g := NewMemoryDoomLoopGuard()
	args := map[string]any{"path": "portrait.svg"}
	for round := 1; round <= DoomLoopMaxSameCodeRejects; round++ {
		response := fmt.Sprint(round)
		for range 8 {
			allowed, count, code, err := g.Check(t.Context(), "s", response, "view_image", args)
			testutil.FailErr(t, "check batch", err)
			if !allowed || count != round-1 || code != "" {
				t.Fatalf("same-response feedback affected check: %v %d %s", allowed, count, code)
			}
			testutil.FailErr(t, "record batch", g.RecordAttempt(t.Context(), "s", response, "view_image", args, "TOOL_NOT_OFFERED", false))
		}
	}
	allowed, count, code, err := g.Check(t.Context(), "s", "next", "view_image", args)
	testutil.FailErr(t, "check retry after feedback", err)
	if allowed || count != DoomLoopMaxSameCodeRejects || code != "TOOL_NOT_OFFERED" {
		t.Fatalf("retry after feedback escaped guard: %v %d %s", allowed, count, code)
	}
}

func TestSuccessfulBatchCountsOneResponse(t *testing.T) {
	g := NewMemoryDoomLoopGuard()
	args := map[string]any{"path": "README.md"}
	for round := range DoomLoopMaxAttempts {
		response := fmt.Sprint(round)
		for range 12 {
			allowed, count, _, err := g.Check(t.Context(), "s", response, "read", args)
			testutil.FailErr(t, "check read", err)
			if !allowed || count != round {
				t.Fatalf("batch count = %d, allowed = %v", count, allowed)
			}
			testutil.FailErr(t, "record read", g.RecordAttempt(t.Context(), "s", response, "read", args, "", false))
		}
	}
	allowed, count, _, err := g.Check(t.Context(), "s", "next", "read", args)
	testutil.FailErr(t, "check repeated response", err)
	if allowed || count != DoomLoopMaxAttempts {
		t.Fatalf("repeated responses escaped guard: allowed=%v count=%d", allowed, count)
	}
}

func TestResolvedConstraintReleasesIdenticalRetry(t *testing.T) {
	for _, code := range []string{"TOOL_NOT_OFFERED", "WRITE_SCOPE_DENIED"} {
		t.Run(code, func(t *testing.T) {
			g := NewMemoryDoomLoopGuard()
			args := map[string]any{"path": "portrait.svg"}
			for response := range DoomLoopMaxAttempts {
				testutil.FailErr(t, "record rejected response", g.RecordAttempt(t.Context(), "s", fmt.Sprint(response), "view_image", args, code, false))
			}
			testutil.FailErr(t, "resolve schema availability", g.ResolveRejection(t.Context(), "s", "view_image", args, "TOOL_NOT_OFFERED"))
			allowed, count, _, err := g.Check(t.Context(), "s", "loaded", "view_image", args)
			testutil.FailErr(t, "check recovered call", err)
			if code == "TOOL_NOT_OFFERED" {
				if !allowed || count != 0 {
					t.Fatalf("resolved constraint still blocks: %v %d", allowed, count)
				}
			} else if allowed {
				t.Fatal("schema loading cleared an unrelated rejection")
			}
			if got := g.CodeRejectResponses("s", "view_image", code); got != DoomLoopMaxCodeRepeats {
				t.Fatalf("lost rejection history: %d", got)
			}
		})
	}
}

func TestDelayedRejectionDoesNotCountResponseTwice(t *testing.T) {
	g := NewMemoryDoomLoopGuard()
	for _, response := range []string{"first", "second", "first"} {
		testutil.FailErr(t, "record delayed rejection", g.RecordAttempt(t.Context(), "s", response, "read", map[string]any{"path": response}, "PATH_DENIED", false))
	}
	if got := g.CodeRejectResponses("s", "read", "PATH_DENIED"); got != 2 {
		t.Fatalf("responses = %d, want 2", got)
	}
}

func TestRejectedResponseTrackingIsBounded(t *testing.T) {
	g := NewMemoryDoomLoopGuard()
	for response := range 100 {
		testutil.FailErr(t, "record rejection", g.RecordAttempt(t.Context(), "s", fmt.Sprint(response), "view_image", map[string]any{"path": "portrait.svg"}, "TOOL_NOT_OFFERED", false))
	}
	if got := g.CodeRejectResponses("s", "view_image", "TOOL_NOT_OFFERED"); got != DoomLoopMaxCodeRepeats {
		t.Fatalf("bounded count = %d", got)
	}
	totals, _ := g.codeRuns.Load("s")
	if len(totals[doomLoopCodeKey("view_image", "TOOL_NOT_OFFERED")]) != DoomLoopMaxCodeRepeats {
		t.Fatal("response identities exceeded the bound")
	}
}
