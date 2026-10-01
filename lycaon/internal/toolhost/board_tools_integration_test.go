//go:build integration

package toolhost

import (
	"context"
	"github.com/lycaon/lycaon/internal/projectroot"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
)

func boardToolFixture(t *testing.T) (*tools.DefaultToolExecutor, *tools.DefaultRegistry) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")

	rt, err := NewRuntime(RuntimeConfig{ConfigRoot: root, Catalog: extpackstest.StockCatalog(t)})
	testutil.FailErr(t, "NewRuntime failed", err)
	builder := &board.SnapshotBuilder{
		Delegations: delegation.NewMemoryStore(),
		Workers:     worker.NewInMemoryQueue(10),
		Repo:        repotest.NewProvider(t),
	}
	if err := board.RegisterBoardTools(rt.Registry, board.ToolDeps{
		Builder: builder,
	}); err != nil {
		t.Fatal(err)
	}
	return rt.Executor, rt.Registry
}

func TestCoordinatorProfileListsBoardTools(t *testing.T) {
	exec, _ := boardToolFixture(t)
	tools := tools.ListToolsForProfile(context.Background(), exec, platform.ToolFilter{ProfileID: "coordinator"})
	names := make(map[string]bool, len(tools))
	for _, meta := range tools {
		names[meta.Name] = true
	}
	if !names["pack_board"] {
		t.Fatal("coordinator profile missing tool \"pack_board\"")
	}
}

func TestPackBoardToolReturnsEnvelope(t *testing.T) {
	_, reg := boardToolFixture(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module packboard\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	raw, err := reg.Run(context.Background(), "pack_board", map[string]any{"detail_level": "compact"}, tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}},
		ActiveRootID: "r1",
		SessionID:    "sess-1",
		Agent:        "coordinator",
	})
	testutil.FailErr(t, "reg.Run failed", err)
	if !strings.Contains(raw, `"board"`) || !strings.Contains(raw, `"now_line"`) {
		t.Fatalf("envelope missing fields: %s", raw)
	}
}
