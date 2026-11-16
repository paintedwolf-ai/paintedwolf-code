package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/board"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"github.com/lycaon/lycaon/internal/tools"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCatalogAllowlistIncludesPackBoard(t *testing.T) {
	cfg, err := tools.LoadToolsConfig()
	contractcheck.FailErr(t, "tools.LoadToolsConfig failed", err)
	allowed := map[string]bool{}
	for _, name := range cfg.Tools {
		allowed[name] = true
	}
	for _, toolName := range []string{"pack_board", "read_findings"} {
		if !allowed[toolName] {
			t.Fatalf("%q missing from lycaon-tools.yaml", toolName)
		}
	}

	builder := &board.SnapshotBuilder{Repo: repotest.NewProvider(t)}
	reg := tools.NewDefaultRegistry()
	if err := board.RegisterBoardTools(reg, board.ToolDeps{Builder: builder}); err != nil {
		contractcheck.FailErr(t, "board.RegisterBoardTools failed", err)
	}
	for _, toolName := range []string{"pack_board", "read_findings"} {
		meta, ok := reg.Meta(toolName)
		if !ok {
			t.Fatalf("tool %q not registered", toolName)
		}
		if meta.ArgsSchema == nil {
			t.Fatalf("tool %q missing args schema", toolName)
		}
	}
}
