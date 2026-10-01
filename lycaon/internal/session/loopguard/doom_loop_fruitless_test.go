package loopguard

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Reworded search scope shares the same fruitless-question counter.
func TestFruitlessSearchRunSurvivesScopeRewording(t *testing.T) {
	g := NewMemoryDoomLoopGuard()
	ctx := context.Background()
	pattern := "hardenSpawn|reapProcessGroup"

	rewordings := []map[string]any{
		{"pattern": pattern, "path": "lycaon", "path_glob": "*.go"},
		{"pattern": pattern, "path": "lycaon"},
		{"pattern": pattern, "path": "lycaon", "include_hidden": true},
		{"pattern": pattern, "path": "lycaon/internal", "path_glob": "*.go", "max_matches": 50},
	}
	for i, args := range rewordings {
		run, err := g.RecordSearchOutcome(ctx, "s1", "grep", args, false)
		testutil.FailErr(t, "record fruitless", err)
		if run != i+1 {
			t.Fatalf("rewording %d: run = %d want %d (scope changes must not split the run)", i, run, i+1)
		}
	}
}

// A search yielding evidence resets the fruitless run.
func TestFruitlessSearchRunResetsOnMaterial(t *testing.T) {
	g := NewMemoryDoomLoopGuard()
	ctx := context.Background()
	args := map[string]any{"pattern": "mcpProjectDir", "path": "lycaon"}

	for i := 0; i < 3; i++ {
		if _, err := g.RecordSearchOutcome(ctx, "s1", "grep", args, false); err != nil {
			testutil.FailErr(t, "record fruitless", err)
		}
	}
	run, err := g.RecordSearchOutcome(ctx, "s1", "grep", map[string]any{
		"pattern": "mcpProjectDir", "path": "lycaon/internal/api",
	}, true)
	testutil.FailErr(t, "record found", err)
	if run != 0 {
		t.Fatalf("finding material must reset the run, got %d", run)
	}
	if got := g.FruitlessSearchRun("s1", "grep", args); got != 0 {
		t.Fatalf("run after reset = %d want 0", got)
	}
}

// TestFruitlessSearchRunIsPerQuestion keeps distinct searches independent — two
// different patterns coming up empty are two facts, not one loop.
func TestFruitlessSearchRunIsPerQuestion(t *testing.T) {
	g := NewMemoryDoomLoopGuard()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := g.RecordSearchOutcome(ctx, "s1", "grep", map[string]any{"pattern": "alpha"}, false); err != nil {
			testutil.FailErr(t, "record alpha", err)
		}
	}
	run, err := g.RecordSearchOutcome(ctx, "s1", "grep", map[string]any{"pattern": "beta"}, false)
	testutil.FailErr(t, "record beta", err)
	if run != 1 {
		t.Fatalf("second question run = %d want 1", run)
	}
}

// TestFruitlessSearchUntrackedTools keeps the counter to tools with a real
// question/scope split; a read has no pattern to circle.
func TestFruitlessSearchUntrackedTools(t *testing.T) {
	g := NewMemoryDoomLoopGuard()
	ctx := context.Background()
	run, err := g.RecordSearchOutcome(ctx, "s1", "read", map[string]any{"path": "README.md"}, false)
	testutil.FailErr(t, "record read", err)
	if run != 0 {
		t.Fatalf("read must not be tracked, run = %d", run)
	}
	if got := g.FruitlessSearchRun("s1", "grep", map[string]any{"path": "lycaon"}); got != 0 {
		t.Fatalf("grep with no pattern must not be tracked, run = %d", got)
	}
}
