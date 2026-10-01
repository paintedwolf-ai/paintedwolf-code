package loopguard

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func buildArgs() map[string]any {
	return map[string]any{"command": "go build -o bin .", "cwd": "reloader"}
}

func editArgs(path string) map[string]any {
	return map[string]any{"path": path, "old": "v1", "new": "v2"}
}

// The edit/build/run cycle issues byte-identical build args every time around,
// and each of those calls is correct.
func TestRebuildAfterEditIsNotARepeat(t *testing.T) {
	ctx := context.Background()
	g := NewMemoryDoomLoopGuard()

	for round := range 6 {
		if err := g.RecordAttempt(ctx, "sess", uuid.NewString(), "edit", editArgs("main.go"), "", true); err != nil {
			t.Fatalf("round %d record edit: %v", round, err)
		}
		allowed, count, _, err := g.Check(ctx, "sess", uuid.NewString(), "command", buildArgs())
		if err != nil {
			t.Fatalf("round %d check build: %v", round, err)
		}
		if !allowed {
			t.Fatalf("round %d blocked a rebuild after an edit", round)
		}
		if count != 0 {
			t.Fatalf("round %d rebuild counted as repeat %d", round, count)
		}
		if err := g.RecordAttempt(ctx, "sess", uuid.NewString(), "command", buildArgs(), "", true); err != nil {
			t.Fatalf("round %d record build: %v", round, err)
		}
	}
}

// Two effectful calls alternating are the shape of a real dev loop: build, then
// restart what was built. Neither is a repeat of itself.
func TestBuildAndRestartAlternatingAreNotRepeats(t *testing.T) {
	ctx := context.Background()
	g := NewMemoryDoomLoopGuard()
	run := map[string]any{"command": "./bin ./sample", "background": true}

	for round := range 6 {
		for _, step := range []struct {
			tool string
			args map[string]any
		}{{"command", buildArgs()}, {"command", run}} {
			_, count, _, err := g.Check(ctx, "sess", uuid.NewString(), step.tool, step.args)
			if err != nil {
				t.Fatalf("round %d check: %v", round, err)
			}
			if count != 0 {
				t.Fatalf("round %d %v counted as repeat %d", round, step.args["command"], count)
			}
			if err := g.RecordAttempt(ctx, "sess", uuid.NewString(), step.tool, step.args, "", true); err != nil {
				t.Fatalf("round %d record: %v", round, err)
			}
		}
	}
}

// An invocation repeated with nothing in between still reaches the block.
func TestUninterruptedRepeatStillBlocks(t *testing.T) {
	ctx := context.Background()
	g := NewMemoryDoomLoopGuard()

	for i := range DoomLoopMaxAttempts {
		allowed, count, _, err := g.Check(ctx, "sess", uuid.NewString(), "command", buildArgs())
		if err != nil {
			t.Fatalf("attempt %d check: %v", i, err)
		}
		if !allowed {
			t.Fatalf("attempt %d blocked before the cap at %d", i, DoomLoopMaxAttempts)
		}
		if count != i {
			t.Fatalf("attempt %d counted %d", i, count)
		}
		if err := g.RecordAttempt(ctx, "sess", uuid.NewString(), "command", buildArgs(), "", true); err != nil {
			t.Fatalf("attempt %d record: %v", i, err)
		}
	}
	if allowed, count, _, _ := g.Check(ctx, "sess", uuid.NewString(), "command", buildArgs()); allowed {
		t.Fatalf("identical call %d still allowed", count)
	}
}

// Surveying between two identical mutations is not progress, so reads leave the
// count intact.
func TestReadOnlyCallsDoNotClearTheCount(t *testing.T) {
	ctx := context.Background()
	g := NewMemoryDoomLoopGuard()
	write := map[string]any{"path": "main.go", "content": "package main"}

	for i := range DoomLoopMaxAttempts {
		if err := g.RecordAttempt(ctx, "sess", uuid.NewString(), "write", write, "", true); err != nil {
			t.Fatalf("attempt %d record write: %v", i, err)
		}
		if err := g.RecordAttempt(ctx, "sess", uuid.NewString(), "read", map[string]any{"path": "other.go"}, "", false); err != nil {
			t.Fatalf("attempt %d record read: %v", i, err)
		}
	}
	if allowed, count, _, _ := g.Check(ctx, "sess", uuid.NewString(), "write", write); allowed {
		t.Fatalf("repeated write survived %d reads at count %d", DoomLoopMaxAttempts, count)
	}
}

// A rejected call changed nothing, so it cannot clear another bucket's history;
// otherwise a failing tool launders every repeat beside it.
func TestRejectedCallsDoNotClearAnotherBucket(t *testing.T) {
	ctx := context.Background()
	g := NewMemoryDoomLoopGuard()

	for i := range DoomLoopMaxAttempts {
		if err := g.RecordAttempt(ctx, "sess", uuid.NewString(), "command", buildArgs(), "", true); err != nil {
			t.Fatalf("attempt %d record build: %v", i, err)
		}
		if err := g.RecordAttempt(ctx, "sess", uuid.NewString(), "edit", editArgs("missing.go"), "EDIT_NO_MATCH", false); err != nil {
			t.Fatalf("attempt %d record failed edit: %v", i, err)
		}
	}
	if allowed, count, _, _ := g.Check(ctx, "sess", uuid.NewString(), "command", buildArgs()); allowed {
		t.Fatalf("repeated build laundered by failing edits at count %d", count)
	}
}

// Rejects under one Code stop when the inputs move: the fix is often the same
// call after a real change.
func TestSameCodeRunResetsAfterAnInterveningEffect(t *testing.T) {
	ctx := context.Background()
	g := NewMemoryDoomLoopGuard()
	call := map[string]any{"command": "go test ./...", "cwd": "."}

	for i := range DoomLoopMaxSameCodeRejects {
		if err := g.RecordAttempt(ctx, "sess", uuid.NewString(), "command", call, "SANDBOX_TRY_WRITE_ROOT", false); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if allowed, _, code, _ := g.Check(ctx, "sess", uuid.NewString(), "command", call); allowed {
		t.Fatalf("same-Code run did not block, code %q", code)
	}
	if err := g.RecordAttempt(ctx, "sess", uuid.NewString(), "write", map[string]any{"path": "go.mod"}, "", true); err != nil {
		t.Fatalf("record intervening write: %v", err)
	}
	allowed, count, code, _ := g.Check(ctx, "sess", uuid.NewString(), "command", call)
	if !allowed {
		t.Fatalf("blocked after the inputs moved (count %d, code %q)", count, code)
	}
}
