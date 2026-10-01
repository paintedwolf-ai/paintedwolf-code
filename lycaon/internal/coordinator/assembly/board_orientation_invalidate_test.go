package assembly_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/pkg/api"
)

type invalidateStubBuilder struct{}

func (invalidateStubBuilder) BuildBoardSnapshot(_ context.Context, _, _, _ string, _ api.BoardDetailLevel, _ []projectroot.RootRef) (*api.BoardSnapshot, error) {
	return &api.BoardSnapshot{PackContentHash: "same"}, nil
}

type invalidateStubFormatter struct{}

func (invalidateStubFormatter) FormatBoardInject(_ api.BoardSnapshot, _ bool, _ time.Time) (string, bool) {
	return "Repo: stub", true
}

func TestBoardEngineInvalidateOrientationForcesReinject(t *testing.T) {
	engine := assembly.NewBoardEngine(invalidateStubBuilder{}, invalidateStubFormatter{}, nil)
	ctx := context.Background()
	sess := &api.Session{ID: "s1", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: t.TempDir(), AgentType: "coordinator"}
	block, ok := engine.PrependBoardIfChanged(ctx, sess, api.CoordinatorRunContext{CurrentPhase: "plan"})
	if !ok || !strings.Contains(block, "Repo: stub") {
		t.Fatalf("first inject missing: ok=%v block=%q", ok, block)
	}
	block2, ok2 := engine.PrependBoardIfChanged(ctx, sess, api.CoordinatorRunContext{CurrentPhase: "plan"})
	if ok2 {
		t.Fatalf("expected no second inject, got %q", block2)
	}
	engine.InvalidateOrientation(sess.ID)
	block3, ok3 := engine.PrependBoardIfChanged(ctx, sess, api.CoordinatorRunContext{CurrentPhase: "plan"})
	if !ok3 || !strings.Contains(block3, "Repo: stub") {
		t.Fatalf("expected reinject after invalidate: ok=%v block=%q", ok3, block3)
	}
}

func TestBoardEngineReinjectsForEachWorkflowRun(t *testing.T) {
	engine := assembly.NewBoardEngine(invalidateStubBuilder{}, invalidateStubFormatter{}, nil)
	ctx := t.Context()
	sess := &api.Session{ID: "s1", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: t.TempDir(), AgentType: "coordinator"}
	var previousHash string
	for _, runID := range []string{"run-a", "run-b", ""} {
		run := api.CoordinatorRunContext{RunID: runID, CurrentPhase: "boot"}
		if !engine.BoardWillForceInject(ctx, sess, run) {
			t.Fatalf("run %q did not predict orientation injection", runID)
		}
		block, ok := engine.PrependBoardIfChanged(ctx, sess, run)
		if !ok || !strings.Contains(block, "Repo: stub") {
			t.Fatalf("run %q missing orientation: ok=%v block=%q", runID, ok, block)
		}
		hash := engine.BoardInjectHash(sess.ID)
		if hash == "" || hash == previousHash {
			t.Fatalf("run %q reused cache key %q", runID, hash)
		}
		previousHash = hash
		if engine.BoardWillForceInject(ctx, sess, run) {
			t.Fatalf("stable run %q predicted redundant injection", runID)
		}
		if block, ok := engine.PrependBoardIfChanged(ctx, sess, run); ok {
			t.Fatalf("stable run %q repeated orientation: %q", runID, block)
		}
	}
}
