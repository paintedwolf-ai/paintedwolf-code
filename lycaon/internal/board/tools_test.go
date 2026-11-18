package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/worker/workertest"
	"testing"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/projectroot"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPackBoardToolReturnsSnapshot(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	builder := &SnapshotBuilder{
		Workers: workertest.NewQueue(t, 10, testdbseed.DefaultProjectID),
		Repo:    repotest.NewProvider(t),
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
		Workers: workertest.NewQueue(t, 10, testdbseed.DefaultProjectID),
		Repo:    repotest.NewProvider(t),
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

func TestPackBoardToolRejectsRetiredFindingsArgs(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register board tools", RegisterBoardTools(reg, ToolDeps{
		Builder: &SnapshotBuilder{Repo: repotest.NewProvider(t)},
	}))
	for _, field := range []string{"finding_id", "findings_after"} {
		_, err := reg.Run(t.Context(), "pack_board", map[string]any{field: 1}, tools.ToolContext{})
		var rej *toolrejection.ToolReject
		if !errors.As(err, &rej) {
			t.Fatalf("%s: err = %v, want ToolReject", field, err)
		}
		if rej.Code != "TOOL_ARGS_INVALID" || rej.Data["reason"] != "moved_to_read_findings" || rej.Data["replacement_tool"] != "read_findings" {
			t.Fatalf("%s: reject = %+v", field, rej)
		}
	}
}

func TestPackBoardToolPagesFiftyWorkersAndFiltersBeforePaging(t *testing.T) {
	all := make([]api.BoardWorker, 50)
	planSet := map[string]bool{}
	for i := range all {
		name := fmt.Sprintf("worker-%02d", i+1)
		all[i] = api.BoardWorker{Name: name, AgentType: "implementer", State: api.WorkerStateWorking}
		if i%2 == 0 {
			planSet[name] = true
		}
	}
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register board tools", RegisterBoardTools(reg, ToolDeps{
		Builder: &SnapshotBuilder{
			Workers:      workertest.NewQueue(t, 10, testdbseed.DefaultProjectID),
			Repo:         repotest.NewProvider(t),
			NamedWorkers: func(_ context.Context, _ string) ([]api.BoardWorker, error) { return all, nil },
		},
		PlanWorkers: func(_ context.Context, _, plan string) (map[string]bool, error) {
			if plan == "plans/even.md" {
				return planSet, nil
			}
			return nil, nil
		},
	}))
	dir := t.TempDir()
	tctx := tools.ToolContext{
		Source:   tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}}, ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{SessionID: "sess-scale"},
	}

	var seen []string
	cursor := ""
	pages := 0
	for {
		args := map[string]any{}
		if cursor != "" {
			args["cursor"] = cursor
		}
		raw, err := reg.Run(t.Context(), "pack_board", args, tctx)
		testutil.FailErr(t, "pack_board page", err)
		var view api.BoardView
		testutil.FailErr(t, "decode page", json.Unmarshal([]byte(raw), &view))
		if len(view.Workers) > MaxBoardWorkersPage {
			t.Fatalf("page size = %d exceeds %d", len(view.Workers), MaxBoardWorkersPage)
		}
		for _, w := range view.Workers {
			seen = append(seen, w.Name)
		}
		pages++
		if !view.WorkersHasMore {
			break
		}
		cursor = view.WorkersNextCursor
	}
	if pages != 3 || len(seen) != 50 {
		t.Fatalf("pages=%d seen=%d, want 3 and 50", pages, len(seen))
	}

	raw, err := reg.Run(t.Context(), "pack_board", map[string]any{"plan": "plans/even.md"}, tctx)
	testutil.FailErr(t, "pack_board filtered page 1", err)
	var filtered api.BoardView
	testutil.FailErr(t, "decode filtered", json.Unmarshal([]byte(raw), &filtered))
	if len(filtered.Workers) != 20 || !filtered.WorkersHasMore || filtered.WorkersOmitted != 5 || filtered.WorkersNextCursor != "20" {
		t.Fatalf("filtered page 1 = len:%d more:%v omitted:%d next:%q", len(filtered.Workers), filtered.WorkersHasMore, filtered.WorkersOmitted, filtered.WorkersNextCursor)
	}

	for _, bad := range []any{"-1", "abc", 12} {
		if _, err := reg.Run(t.Context(), "pack_board", map[string]any{"cursor": bad}, tctx); err == nil {
			t.Fatalf("cursor %v expected rejection", bad)
		}
	}
}
