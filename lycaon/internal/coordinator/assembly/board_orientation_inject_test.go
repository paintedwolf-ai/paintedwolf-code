package assembly

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/projectroot"
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
