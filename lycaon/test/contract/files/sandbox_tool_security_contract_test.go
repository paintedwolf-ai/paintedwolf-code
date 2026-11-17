package contract

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
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

func TestWriteScopeBlockedSecurity(t *testing.T) {
	t.Parallel()
	b := contractcheck.ProdToolBoundary(t)
	if err := b.AssertToolAllowed(context.Background(), "worker_readonly", "write", sandbox.ToolAccessProfile); err == nil {
		t.Fatal("expected write denied for readonly profile")
	}
}

// Every shipped reader, command's native file reads and summarize alike,
// resolves paths through the production boundary's read gate.
func TestShippedReadResolutionRejectsEscapesSecurity(t *testing.T) {
	t.Parallel()
	root, outside := t.TempDir(), t.TempDir()
	testutil.FailErr(t, "write outside", os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644))
	testutil.FailErr(t, "write gitignore", os.WriteFile(filepath.Join(root, ".gitignore"), []byte("../outside/\n"), 0o644))
	testutil.FailErr(t, "link outside", os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt")))
	b := contractcheck.ProdToolBoundary(t)
	for _, path := range []string{"../outside.txt", "../../../etc/passwd", "src/../../secret.txt", "link.txt", "../outside"} {
		if _, err := projectpaths.ResolveRead(t.Context(), b, implementToolContext(root), path); err == nil {
			t.Errorf("read of %q resolved outside the project", path)
		}
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
