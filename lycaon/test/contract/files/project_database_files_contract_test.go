package contract

import (
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func implementToolContext(root string) tools.ToolContext {
	roots := []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}}
	return tools.ToolContext{
		Source: tools.InvocationSource{Roots: roots,
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	}
}

func TestProjectDatabaseFilesAccessible(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	b := contractcheck.ProdToolBoundary(t)
	write := &native.WriteTool{Boundary: b}
	for _, path := range []string{"data.db", "data.db-wal", "data.db-shm"} {
		_, err := write.Run(t.Context(), map[string]any{"path": path, "content": "fixture bytes\n"}, implementToolContext(root))
		testutil.FailErr(t, "write project database file", err)
		resolved, err := projectpaths.ResolveRead(t.Context(), b, implementToolContext(root), path)
		testutil.FailErr(t, "resolve project database file", err)
		raw, err := os.ReadFile(resolved.Abs)
		testutil.FailErr(t, "read project database file", err)
		if string(raw) != "fixture bytes\n" {
			t.Fatalf("%s content = %q", path, raw)
		}
	}
}
