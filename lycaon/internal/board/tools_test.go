package board

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/projectroot"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"testing"

	"github.com/lycaon/lycaon/internal/delegation"
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
