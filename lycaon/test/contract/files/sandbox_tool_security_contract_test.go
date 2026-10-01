package contract

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	lycexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	surveytools "github.com/lycaon/lycaon/internal/tools/native/survey"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func implementToolContext(root string) tools.ToolContext {
	roots := []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}}
	return tools.ToolContext{Roots: roots, ActiveRootID: "r1", Agent: "implement"}
}

func TestPathEscapeBlockedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	b := contractcheck.ProdToolBoundary(t)
	read := &surveytools.ReadTool{Boundary: b}
	_, err := read.Run(context.Background(), map[string]any{"path": "../outside.txt"}, implementToolContext(root))
	if err == nil {
		t.Fatal("expected path escape error")
	}
}

func TestSymlinkEscapeBlockedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks not supported")
	}

	b := contractcheck.ProdToolBoundary(t)
	read := &surveytools.ReadTool{Boundary: b}
	_, err := read.Run(context.Background(), map[string]any{"path": "link.txt"}, implementToolContext(root))
	if err == nil {
		t.Fatal("expected symlink escape error")
	}
}

func TestWriteScopeBlockedSecurity(t *testing.T) {
	t.Parallel()
	b := contractcheck.ProdToolBoundary(t)
	if err := b.AssertToolAllowed(context.Background(), "worker_readonly", "write", sandbox.ToolAccessProfile); err == nil {
		t.Fatal("expected write denied for readonly profile")
	}
}

func TestVerifyShellMetacharacterRejectionSecurity(t *testing.T) {
	t.Parallel()
	r := hostcmd.NewRunner()
	validate := func(line string) error {
		stages, err := lycexec.StagesFromCommandLine(line)
		if err != nil {
			return err
		}
		return r.ValidateStages(context.Background(), stages)
	}
	// Substitution rejects. Sequences and pipes split into stages, each its own process.
	for _, cmd := range []string{"go test $(id -u)", "go test `id -u`"} {
		if err := validate(cmd); err == nil {
			t.Fatalf("expected rejection for %q", cmd)
		}
	}
	for _, cmd := range []string{"mix test", "go test; go vet", "go test | tee out"} {
		if err := validate(cmd); err != nil {
			t.Fatalf("command should accept arbitrary runners: %q: %v", cmd, err)
		}
	}
}

func TestProjectDatabaseFilesAccessible(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	b := contractcheck.ProdToolBoundary(t)
	write := &native.WriteTool{Boundary: b}
	read := &surveytools.ReadTool{Boundary: b}
	for _, path := range []string{"data.db", "data.db-wal", "data.db-shm"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			_, err := write.Run(t.Context(), map[string]any{
				"path": path, "content": "fixture bytes\n",
			}, implementToolContext(root))
			testutil.FailErr(t, "write project database file", err)
			out, err := read.Run(t.Context(), map[string]any{"path": path}, implementToolContext(root))
			testutil.FailErr(t, "read project database file", err)
			if !strings.Contains(out, "fixture bytes") {
				t.Fatalf("database file content missing: %s", out)
			}
		})
	}
}

func TestFindPathEscapeBlockedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	b := contractcheck.ProdToolBoundary(t)
	find := &surveytools.FindTool{Boundary: b}
	_, err := find.Run(context.Background(), map[string]any{"path": "../outside"}, implementToolContext(root))
	if err == nil {
		t.Fatal("expected path escape error")
	}
}

func TestFindSymlinkEscapeBlockedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.go")
	if err := os.WriteFile(target, []byte("secret"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	linkDir := filepath.Join(root, "pkg")
	if err := os.Mkdir(linkDir, 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	link := filepath.Join(linkDir, "link.go")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks not supported")
	}

	b := contractcheck.ProdToolBoundary(t)
	find := &surveytools.FindTool{Boundary: b}
	out, err := find.Run(context.Background(), map[string]any{"type": "file"}, implementToolContext(root))
	if err != nil {
		testutil.FailErr(t, "find walk", err)
	}
	if strings.Contains(out, "secret") || strings.Contains(out, filepath.Base(outside)) {
		t.Fatalf("find escaped symlink: %q", out)
	}
}

func TestGrepPathEscapeBlockedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	b := contractcheck.ProdToolBoundary(t)
	grep := &surveytools.GrepTool{Boundary: b}
	_, err := grep.Run(context.Background(), map[string]any{
		"pattern": "x",
		"path":    "../outside",
	}, implementToolContext(root))
	if err == nil {
		t.Fatal("expected path escape error")
	}
}

func TestGrepSymlinkEscapeBlockedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(target, []byte("needle\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	linkDir := filepath.Join(root, "pkg")
	if err := os.Mkdir(linkDir, 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	link := filepath.Join(linkDir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks not supported")
	}

	b := contractcheck.ProdToolBoundary(t)
	grep := &surveytools.GrepTool{Boundary: b}
	out, err := grep.Run(context.Background(), map[string]any{"pattern": "needle"}, implementToolContext(root))
	if err != nil {
		testutil.FailErr(t, "grep walk", err)
	}
	if strings.Contains(out, "secret") || strings.Contains(out, filepath.Base(outside)) {
		t.Fatalf("grep escaped symlink: %q", out)
	}
}

func TestFindGitignoredPathEscapeStillBlockedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("../outside/\n"), 0o644); err != nil {
		testutil.FailErr(t, "write .gitignore", err)
	}
	b := contractcheck.ProdToolBoundary(t)
	find := &surveytools.FindTool{Boundary: b}
	_, err := find.Run(context.Background(), map[string]any{"path": "../outside"}, implementToolContext(root))
	if err == nil {
		t.Fatal("expected path escape error despite gitignore entry")
	}
}

func TestGrepGitignoredPathEscapeStillBlockedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("../outside/\n"), 0o644); err != nil {
		testutil.FailErr(t, "write .gitignore", err)
	}
	b := contractcheck.ProdToolBoundary(t)
	grep := &surveytools.GrepTool{Boundary: b}
	_, err := grep.Run(context.Background(), map[string]any{
		"pattern": "x",
		"path":    "../outside",
	}, implementToolContext(root))
	if err == nil {
		t.Fatal("expected path escape error despite gitignore entry")
	}
}

func TestStatPathEscapeBlockedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	b := contractcheck.ProdToolBoundary(t)
	stat := &surveytools.StatTool{Boundary: b}
	_, err := stat.Run(context.Background(), map[string]any{
		"paths": []any{"../outside"},
	}, implementToolContext(root))
	if err == nil {
		t.Fatal("expected path escape error")
	}
}

func TestWcPathEscapeBlockedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	b := contractcheck.ProdToolBoundary(t)
	wc := &surveytools.WcTool{Boundary: b}
	_, err := wc.Run(context.Background(), map[string]any{
		"paths": []any{"../outside"},
	}, implementToolContext(root))
	if err == nil {
		t.Fatal("expected path escape error")
	}
}

func TestListDirPathEscapeBlockedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	b := contractcheck.ProdToolBoundary(t)
	listDir := &surveytools.ListDirTool{Boundary: b}
	_, err := listDir.Run(context.Background(), map[string]any{
		"path": "../outside",
	}, implementToolContext(root))
	if err == nil {
		t.Fatal("expected path escape error")
	}
}
