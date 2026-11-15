package board

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/projectroot"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPackBoardToolReturnsSnapshot(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	builder := &SnapshotBuilder{
		Delegations: delegation.NewMemoryStore(),
		Workers:     worker.NewInMemoryQueue(10),
		Repo:        repotest.NewProvider(t),
	}
	if err := RegisterBoardTools(reg, ToolDeps{Builder: builder}); err != nil {
		testutil.FailErr(t, "RegisterBoardTools failed", err)
	}

	dir := t.TempDir()
	raw, err := reg.Run(context.Background(), "pack_board", map[string]any{"detail_level": "full"}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{SessionID: "sess-1"},
	})
	testutil.FailErr(t, "reg.Run failed", err)
	var env map[string]any
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if env["board"] == nil || env["now_line"] == "" {
		t.Fatalf("envelope missing board/now_line: %v", env)
	}
	if env["detail_level"] != string(api.BoardDetailLevelFull) {
		t.Fatalf("detail_level = %v", env["detail_level"])
	}
}

func TestPackBoardToolFiltersWorkersByPlan(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	builder := &SnapshotBuilder{
		Delegations: delegation.NewMemoryStore(),
		Workers:     worker.NewInMemoryQueue(10),
		Repo:        repotest.NewProvider(t),
		NamedWorkers: func(_ context.Context, _ string) ([]api.BoardWorker, error) {
			return []api.BoardWorker{
				{Name: "leg-auth", AgentType: "implementer", State: api.WorkerStatePlanned},
				{Name: "adhoc-1", AgentType: "explore", State: api.WorkerStateWorking},
			}, nil
		},
	}
	_ = packboard.NamedWorkersFromSnapshot
	testutil.FailErr(t, "RegisterBoardTools", RegisterBoardTools(reg, ToolDeps{
		Builder: builder,
		PlanWorkers: func(_ context.Context, _, plan string) (map[string]bool, error) {
			if plan == "plans/auth.md" {
				return map[string]bool{"leg-auth": true}, nil
			}
			return nil, nil
		},
	}))
	dir := t.TempDir()
	raw, err := reg.Run(t.Context(), "pack_board", map[string]any{"plan": "plans/auth.md"}, tools.ToolContext{
		Source:   tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}}, ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{SessionID: "sess-1"},
	})
	testutil.FailErr(t, "pack_board plan filter", err)
	var view api.BoardView
	testutil.FailErr(t, "decode board view", json.Unmarshal([]byte(raw), &view))
	if len(view.Workers) != 1 || view.Workers[0].Name != "leg-auth" {
		t.Fatalf("plan filter workers = %+v", view.Workers)
	}
}

func TestPackBoardToolRequiresProjectDir(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	builder := &SnapshotBuilder{Repo: repotest.NewProvider(t)}
	if err := RegisterBoardTools(reg, ToolDeps{Builder: builder}); err != nil {
		testutil.FailErr(t, "RegisterBoardTools failed", err)
	}

	_, err := reg.Run(context.Background(), "pack_board", nil, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess-1"},
	})
	if err == nil {
		t.Fatal("expected error for missing project_dir")
	}
}
