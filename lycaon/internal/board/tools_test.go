package board

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/projectroot"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"testing"

	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
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

	for _, field := range []string{"review_view", "assignment_id", "cursor"} {
		t.Run(field, func(t *testing.T) {
			reviews := tools.NewDefaultRegistry()
			calls := 0
			testutil.FailErr(t, "register review view", RegisterBoardTools(reviews, ToolDeps{
				Builder: builder,
				ReviewView: func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
					calls++
					if args[field] != "selected" || tctx.Identity.SessionID != "review-session" || ctx != t.Context() {
						t.Fatalf("review invocation lost its input or context: %v, %+v", args, tctx.Identity)
					}
					return "review result", nil
				},
			}))
			raw, err := reviews.Run(t.Context(), "pack_board", map[string]any{field: "selected"}, tools.ToolContext{
				Identity: tools.InvocationIdentity{SessionID: "review-session"},
			})
			testutil.FailErr(t, "read selected review view", err)
			if raw != "review result" || calls != 1 {
				t.Fatalf("review result = %q, calls = %d; want one selected review", raw, calls)
			}
		})
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
	for _, field := range []string{"review_view", "assignment_id", "cursor"} {
		_, err := reg.Run(t.Context(), "pack_board", map[string]any{field: "selected"}, tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: "sess-1"},
		})
		var rejection *toolrejection.ToolReject
		if !errors.As(err, &rejection) || rejection.Code != "WORKFLOW_REVIEW_VIEW_INVALID" ||
			rejection.Data["reason"] != "review_unavailable" || rejection.Data["field"] != "review_view" {
			t.Fatalf("unavailable %s returned %v; want structured review refusal", field, err)
		}
	}
}
