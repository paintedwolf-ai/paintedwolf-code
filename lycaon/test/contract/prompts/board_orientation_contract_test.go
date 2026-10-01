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
	if !allowed["pack_board"] {
		t.Fatal("\"pack_board\" missing from lycaon-tools.yaml")
	}

	builder := &board.SnapshotBuilder{Repo: repotest.NewProvider(t)}
	reg := tools.NewDefaultRegistry()
	if err := board.RegisterBoardTools(reg, board.ToolDeps{Builder: builder}); err != nil {
		contractcheck.FailErr(t, "board.RegisterBoardTools failed", err)
	}
	meta, ok := reg.Meta("pack_board")
	if !ok {
		t.Fatal("tool \"pack_board\" not registered")
	}
	if meta.ArgsSchema == nil {
		t.Fatal("tool \"pack_board\" missing args schema")
	}
}
