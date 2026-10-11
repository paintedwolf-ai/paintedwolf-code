package contract

import (
	"go/ast"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestHostConstructionLineBudget(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lines := 0
	for _, name := range []string{"host.go", "coordinator_wire.go"} {
		path := filepath.Join(root, "lycaon", "internal", "session", name)
		data, err := os.ReadFile(path)
		contractcheck.FailErr(t, "read constructor "+name, err)
		lines += strings.Count(string(data), "\n") + 1
	}
	const maxLines = 400
	if lines > maxLines {
		t.Fatalf("Host construction has %d lines, want <= %d", lines, maxLines)
	}
}

func TestListToolsForPromptRemovedFromManager(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	sessionDir := filepath.Join(root, "lycaon", "internal", "session")
	fset, files := contractcheck.ParseNonTestGoTree(t, sessionDir)
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Name == nil {
				return true
			}
			if fn.Name.Name == "ListToolsForPrompt" {
				t.Fatalf("Manager must not define ListToolsForPrompt in %s", fset.Position(fn.Pos()).Filename)
			}
			return true
		})
	}
}

func TestSingleCompleteStreamImplementation(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	coordinatorDir := filepath.Join(root, "lycaon", "internal", "coordinator")
	sessionDir := filepath.Join(root, "lycaon", "internal", "session")
	var bodies []string
	for _, dir := range []string{coordinatorDir, sessionDir} {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(data), "func (") && strings.Contains(string(data), " completeStream(") {
				rel, err := filepath.Rel(coordinatorDir, path)
				if err != nil {
					return err
				}
				bodies = append(bodies, rel)
			}
			return nil
		})
		contractcheck.FailErr(t, "operation failed", err)
	}
	if len(bodies) != 1 || bodies[0] != "promptloop/stream.go" {
		t.Fatalf("expected single completeStream in coordinator/promptloop/stream.go, found %v", bodies)
	}
}
