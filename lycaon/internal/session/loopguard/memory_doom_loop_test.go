package loopguard

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMemoryDoomLoopGuardBlocksAfterMaxIdenticalCompletion(t *testing.T) {
	ctx := context.Background()
	g := NewMemoryDoomLoopGuard()
	const sid = "sess-1"
	tool := "read"
	args := map[string]any{"path": "main.go"}

	for attempt := 1; attempt <= DoomLoopMaxAttempts; attempt++ {
		allowed, count, _, err := g.Check(ctx, sid, uuid.NewString(), tool, args)
		testutil.FailErr(t, "g.Check failed", err)
		if !allowed {
			t.Fatalf("attempt %d: expected allowed, count=%d", attempt, count)
		}
		if count != attempt-1 {
			t.Fatalf("attempt %d: count=%d want %d", attempt, count, attempt-1)
		}
		if err := g.RecordAttempt(ctx, sid, uuid.NewString(), tool, args, "", false); err != nil {
			testutil.FailErr(t, "g.RecordAttempt failed", err)
		}
	}

	allowed, count, _, err := g.Check(ctx, sid, uuid.NewString(), tool, args)
	testutil.FailErr(t, "g.Check failed", err)
	if allowed {
		t.Fatalf("attempt after max should be blocked, count=%d", count)
	}
	if count != DoomLoopMaxAttempts {
		t.Fatalf("count=%d want %d", count, DoomLoopMaxAttempts)
	}
}

func TestMemoryDoomLoopGuardDifferentArgsAllowed(t *testing.T) {
	ctx := context.Background()
	g := NewMemoryDoomLoopGuard()
	const sid = "sess-1"
	tool := "read"

	for i := 0; i < 5; i++ {
		args := map[string]any{"path": "file-" + string(rune('a'+i)) + ".go"}
		allowed, _, _, err := g.Check(ctx, sid, uuid.NewString(), tool, args)
		testutil.FailErr(t, "g.Check failed", err)
		if !allowed {
			t.Fatalf("distinct args should stay allowed: %+v", args)
		}
		if err := g.RecordAttempt(ctx, sid, uuid.NewString(), tool, args, "", false); err != nil {
			testutil.FailErr(t, "g.RecordAttempt failed", err)
		}
	}
}

func TestMemoryDoomLoopGuardSummarizeIgnoresTaskParaphrase(t *testing.T) {
	ctx := context.Background()
	g := NewMemoryDoomLoopGuard()
	const sid = "sess-sum"
	pathArgs := map[string]any{"path": "lycaon/AGENTS.md", "task": "Summarize the contents of this policy file"}
	for i := 0; i < DoomLoopMaxAttempts; i++ {
		if err := g.RecordAttempt(ctx, sid, uuid.NewString(), "summarize", pathArgs, "", false); err != nil {
			testutil.FailErr(t, "RecordAttempt", err)
		}
	}
	// Same path, rephrased task: shares the fingerprint and hard-blocks.
	paraphrase := map[string]any{"path": "lycaon/AGENTS.md", "task": "Summarize this file in a few anchors"}
	allowed, count, _, err := g.Check(ctx, sid, uuid.NewString(), "summarize", paraphrase)
	testutil.FailErr(t, "Check paraphrase", err)
	if allowed {
		t.Fatalf("summarize task paraphrase should share doom key, count=%d", count)
	}
	// Different path stays allowed.
	other := map[string]any{"path": "lycaon/go.mod", "task": "Summarize the contents of this policy file"}
	allowed, _, _, err = g.Check(ctx, sid, uuid.NewString(), "summarize", other)
	testutil.FailErr(t, "Check other path", err)
	if !allowed {
		t.Fatal("different summarize path must not share doom key")
	}
}

func TestMemoryDoomLoopGuardCapturePageIgnoresCaptionAndWaitPadding(t *testing.T) {
	ctx := context.Background()
	g := NewMemoryDoomLoopGuard()
	const sid = "sess-cap"
	base := map[string]any{
		"project_dir": ".",
		"caption":     "Initial board",
		"actions":     []any{map[string]any{"type": "wait", "wait": "idle"}},
	}
	for i := 0; i < DoomLoopMaxAttempts; i++ {
		if err := g.RecordAttempt(ctx, sid, uuid.NewString(), "capture_page", base, "", false); err != nil {
			testutil.FailErr(t, "RecordAttempt", err)
		}
	}
	// A caption paraphrase plus extra wait-only steps shares the fingerprint.
	padded := map[string]any{
		"project_dir": ".",
		"caption":     "New game reset",
		"actions": []any{
			map[string]any{"type": "wait", "wait": "idle"},
			map[string]any{"type": "wait", "wait": "idle"},
		},
	}
	allowed, count, _, err := g.Check(ctx, sid, uuid.NewString(), "capture_page", padded)
	testutil.FailErr(t, "Check padded capture", err)
	if allowed {
		t.Fatalf("wait-padded capture_page should share doom key, count=%d", count)
	}
	// A material click action stays allowed.
	driven := map[string]any{
		"project_dir": ".",
		"caption":     "After move",
		"actions": []any{
			map[string]any{"type": "click", "selector": ".cell:nth-child(5)"},
			map[string]any{"type": "wait", "wait": "idle"},
		},
	}
	allowed, _, _, err = g.Check(ctx, sid, uuid.NewString(), "capture_page", driven)
	testutil.FailErr(t, "Check driven capture", err)
	if !allowed {
		t.Fatal("capture_page with a click action should stay allowed")
	}
}

func TestMemoryDoomLoopGuardSessionsIsolated(t *testing.T) {
	ctx := context.Background()
	g := NewMemoryDoomLoopGuard()
	tool := "edit"
	args := map[string]any{"path": "x.go"}

	for i := 0; i < DoomLoopMaxAttempts; i++ {
		if err := g.RecordAttempt(ctx, "a", uuid.NewString(), tool, args, "", false); err != nil {
			testutil.FailErr(t, "g.RecordAttempt failed", err)
		}
	}
	allowed, _, _, err := g.Check(ctx, "a", uuid.NewString(), tool, args)
	testutil.FailErr(t, "g.Check failed", err)
	if allowed {
		t.Fatal("session a should be blocked after max identical attempts")
	}

	allowed, count, _, err := g.Check(ctx, "b", uuid.NewString(), tool, args)
	testutil.FailErr(t, "g.Check failed", err)
	if !allowed || count != 0 {
		t.Fatalf("session b should start fresh: allowed=%v count=%d", allowed, count)
	}
}

func TestMemoryDoomLoopGuardContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g := NewMemoryDoomLoopGuard()

	if _, _, _, err := g.Check(ctx, "s", uuid.NewString(), "read", map[string]any{}); err == nil {
		t.Fatal("Check: expected context error")
	}
	if err := g.RecordAttempt(ctx, "s", uuid.NewString(), "read", map[string]any{}, "", false); err == nil {
		t.Fatal("RecordAttempt: expected context error")
	}
}

func TestMemoryDoomLoopGuardBlocksThirdSameCodeReject(t *testing.T) {
	ctx := context.Background()
	g := NewMemoryDoomLoopGuard()
	const sid = "sess-code"
	tool := "write"
	args := map[string]any{}

	for i := 0; i < DoomLoopMaxSameCodeRejects; i++ {
		allowed, _, _, err := g.Check(ctx, sid, uuid.NewString(), tool, args)
		testutil.FailErr(t, "g.Check failed", err)
		if !allowed {
			t.Fatalf("reject %d: expected allowed before the run completes", i+1)
		}
		testutil.FailErr(t, "g.RecordAttempt failed", g.RecordAttempt(ctx, sid, uuid.NewString(), tool, args, "TOOL_ARGS_TRUNCATED", false))
	}

	allowed, count, repeatedCode, err := g.Check(ctx, sid, uuid.NewString(), tool, args)
	testutil.FailErr(t, "g.Check failed", err)
	if allowed {
		t.Fatalf("third identical same-code reject should be blocked, count=%d", count)
	}
	if repeatedCode != "TOOL_ARGS_TRUNCATED" {
		t.Fatalf("repeatedCode = %q want TOOL_ARGS_TRUNCATED", repeatedCode)
	}
}

func TestMemoryDoomLoopGuardCodeChangeResetsRun(t *testing.T) {
	ctx := context.Background()
	g := NewMemoryDoomLoopGuard()
	const sid = "sess-mixed"
	tool := "write"
	args := map[string]any{}

	testutil.FailErr(t, "record", g.RecordAttempt(ctx, sid, uuid.NewString(), tool, args, "TOOL_ARGS_TRUNCATED", false))
	testutil.FailErr(t, "record", g.RecordAttempt(ctx, sid, uuid.NewString(), tool, args, "WRITE_SCOPE_DENIED", false))
	allowed, _, repeatedCode, err := g.Check(ctx, sid, uuid.NewString(), tool, args)
	testutil.FailErr(t, "g.Check failed", err)
	if !allowed || repeatedCode != "" {
		t.Fatalf("alternating codes must not block: allowed=%v code=%q", allowed, repeatedCode)
	}
}

func TestMemoryDoomLoopGuardCompletionResetsCodeRun(t *testing.T) {
	ctx := context.Background()
	g := NewMemoryDoomLoopGuard()
	const sid = "sess-reset"
	tool := "write"
	args := map[string]any{}

	testutil.FailErr(t, "record", g.RecordAttempt(ctx, sid, uuid.NewString(), tool, args, "TOOL_ARGS_TRUNCATED", false))
	testutil.FailErr(t, "record", g.RecordAttempt(ctx, sid, uuid.NewString(), tool, args, "", false))
	testutil.FailErr(t, "record", g.RecordAttempt(ctx, sid, uuid.NewString(), tool, args, "TOOL_ARGS_TRUNCATED", false))
	allowed, _, repeatedCode, err := g.Check(ctx, sid, uuid.NewString(), tool, args)
	testutil.FailErr(t, "g.Check failed", err)
	if !allowed || repeatedCode != "" {
		t.Fatalf("completion between rejects must reset the run: allowed=%v code=%q", allowed, repeatedCode)
	}
}

var _ DoomLoopGuard = (*MemoryDoomLoopGuard)(nil)

func TestMemoryDoomLoopGuardExemptsTerminalSend(t *testing.T) {
	ctx := context.Background()
	g := NewMemoryDoomLoopGuard()
	const sid = "sess-pty"
	args := map[string]any{"id": "pty-1", "input": "{Enter}"}
	for i := 0; i < DoomLoopMaxAttempts+5; i++ {
		allowed, count, _, err := g.Check(ctx, sid, uuid.NewString(), "terminal_send", args)
		testutil.FailErr(t, "Check", err)
		if !allowed {
			t.Fatalf("terminal_send must stay allowed after %d identical keystrokes, count=%d", i, count)
		}
		if err := g.RecordAttempt(ctx, sid, uuid.NewString(), "terminal_send", args, "", false); err != nil {
			testutil.FailErr(t, "RecordAttempt", err)
		}
	}
}

func TestMemoryDoomLoopGuardPageIDResolvesToTarget(t *testing.T) {
	ctx := context.Background()
	g := NewMemoryDoomLoopGuard()
	const sid = "sess-page"
	// page-a-again is what a reopen of page-a's target mints.
	targets := map[string]string{
		"page-a":       "http://127.0.0.1:8765/",
		"page-a-again": "http://127.0.0.1:8765/",
		"page-b":       "http://127.0.0.1:9000/",
	}
	g.SetPageTargetResolver(func(_, pageID string) string { return targets[pageID] })

	snap := func(id, caption string) map[string]any {
		return map[string]any{"id": id, "caption": caption}
	}
	for i := 0; i < DoomLoopMaxAttempts; i++ {
		if err := g.RecordAttempt(ctx, sid, uuid.NewString(), "page_snapshot", snap("page-a", "before reload"), "", false); err != nil {
			testutil.FailErr(t, "RecordAttempt", err)
		}
	}

	// Neither the fresh id nor the reworded caption may reset the count.
	allowed, count, _, err := g.Check(ctx, sid, uuid.NewString(), "page_snapshot", snap("page-a-again", "status UI before reload"))
	testutil.FailErr(t, "Check reopened page", err)
	if allowed {
		t.Fatalf("reopened page on the same target must share the doom key, count=%d", count)
	}

	// A different live page is its own bucket.
	allowed, _, _, err = g.Check(ctx, sid, uuid.NewString(), "page_snapshot", snap("page-b", "other app"))
	testutil.FailErr(t, "Check second target", err)
	if !allowed {
		t.Fatal("a snapshot of a different live page must not inherit another page's count")
	}
}

func TestMemoryDoomLoopGuardPageFingerprintLeavesArgsIntact(t *testing.T) {
	ctx := context.Background()
	g := NewMemoryDoomLoopGuard()
	g.SetPageTargetResolver(func(_, _ string) string { return "http://127.0.0.1:8765/" })
	args := map[string]any{"id": "page-a"}
	if err := g.RecordAttempt(ctx, "sess-args", uuid.NewString(), "page_close", args, "", false); err != nil {
		testutil.FailErr(t, "RecordAttempt", err)
	}
	if _, ok := args["page_target"]; ok {
		t.Fatal("fingerprinting must not write into the caller's args map")
	}
	if got, _ := args["id"].(string); got != "page-a" {
		t.Fatalf("caller args mutated: id=%q", got)
	}
}
