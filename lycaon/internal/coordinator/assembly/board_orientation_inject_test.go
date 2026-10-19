package assembly

import (
	"context"
	"github.com/lycaon/lycaon/internal/projectroot"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBoardEngineWorkerBoardRefreshesEachRequest(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	engine := NewBoardEngine(orientationStubBoardBuilder{hash: "abc"}, nil, nil)
	engine.SetInjectRenderer(renderer)
	ctx := context.Background()
	sess := &api.Session{ID: "w1", ParentSessionID: "p1", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: t.TempDir(), AgentType: "implementer"}
	block, ok := engine.WorkerBoard(ctx, sess)
	if !ok || !strings.Contains(block, packboard.PackBoardSentinel) {
		t.Fatalf("block = %q ok=%v", block, ok)
	}
	block2, ok2 := engine.WorkerBoard(ctx, sess)
	if !ok2 || !strings.Contains(block2, packboard.PackBoardSentinel) {
		t.Fatalf("expected refreshed worker board, got %q", block2)
	}
}

func TestBoardEnginePrependUsesInjectRenderer(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	engine := NewBoardEngine(orientationStubBoardBuilder{hash: "abc"}, nil, nil)
	engine.SetInjectRenderer(renderer)
	ctx := context.Background()
	sess := &api.Session{ID: "s1", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: t.TempDir(), AgentType: "coordinator"}
	block, ok := engine.PrependBoardIfChanged(ctx, sess, api.CoordinatorRunContext{CurrentPhase: "plan"})
	if !ok || !strings.Contains(block, inject.BoardOrientationInjectSentinel) {
		t.Fatalf("block = %q ok=%v", block, ok)
	}
}

type orientationStubBoardBuilder struct{ hash string }

func (s orientationStubBoardBuilder) BuildBoardSnapshot(_ context.Context, _, _, _ string, _ api.BoardDetailLevel, _ []projectroot.RootRef) (*api.BoardSnapshot, error) {
	return &api.BoardSnapshot{PackContentHash: s.hash}, nil
}

func TestBoardEngineReplacesEmptyOrientationWhenFreshnessChanges(t *testing.T) {
	snapshot := &api.BoardSnapshot{Repo: api.RepoBrief{GeneratedAt: time.Unix(1, 0)}}
	engine := NewBoardEngine(changingOrientationBuilder{snapshot}, nil, nil)
	engine.SetInjectRenderer(promptstest.InjectRenderer(t))
	sess := &api.Session{ID: "freshness", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: t.TempDir(), AgentType: "coordinator"}
	run := api.CoordinatorRunContext{CurrentPhase: "work"}
	block, ok := engine.PrependBoardIfChanged(t.Context(), sess, run)
	if !ok || !strings.Contains(block, packboard.RepoEmptyOrientationLine) {
		t.Fatalf("initial empty orientation: ok=%v block=%q", ok, block)
	}
	snapshot.Repo.Incomplete, snapshot.Repo.Refreshing = true, true
	block, ok = engine.PrependBoardIfChanged(t.Context(), sess, run)
	if !ok || !strings.Contains(block, "Repo: file inventory incomplete") || strings.Contains(block, packboard.RepoEmptyOrientationLine) {
		t.Fatalf("stale empty orientation not replaced: ok=%v block=%q", ok, block)
	}
	if block, ok = engine.PrependBoardIfChanged(t.Context(), sess, run); ok {
		t.Fatalf("unchanged freshness reinjected: %q", block)
	}
	snapshot.Repo.Incomplete, snapshot.Repo.Refreshing = false, false
	block, ok = engine.PrependBoardIfChanged(t.Context(), sess, run)
	if !ok || !strings.Contains(block, packboard.RepoEmptyOrientationLine) {
		t.Fatalf("settled empty orientation not restored: ok=%v block=%q", ok, block)
	}
}

type changingOrientationBuilder struct{ snapshot *api.BoardSnapshot }

func (b changingOrientationBuilder) BuildBoardSnapshot(context.Context, string, string, string, api.BoardDetailLevel, []projectroot.RootRef) (*api.BoardSnapshot, error) {
	return b.snapshot, nil
}
