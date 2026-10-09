package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestInformCatalogBound(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	catalogIDs, _ := loadCatalogAnchors(t, root)
	reg := catalogfixture.LoadInformBindings(t)
	var missing []string
	for id := range catalogIDs {
		if !strings.HasPrefix(id, "inject.") && id != "board.changed" {
			continue
		}
		if id == "loop.wake" {
			continue
		}
		// Only inform-content assembly injects / board — skip non-inject catalog rows.
		planesOK := id == "board.changed" || strings.HasPrefix(id, "inject.")
		if !planesOK {
			continue
		}
		b, err := reg.ResolveInform(anchor.ID(id), anchor.MatchContext{})
		if err != nil || b == nil || strings.TrimSpace(b.Render) == "" {
			// Try surface-scoped selectors used by Bindings.
			for _, surface := range []string{"coordinator", "worker"} {
				b, err = reg.ResolveInform(anchor.ID(id), anchor.MatchContext{Surface: surface})
				if err == nil && b != nil && strings.TrimSpace(b.Render) != "" {
					break
				}
			}
		}
		if b == nil || strings.TrimSpace(b.Render) == "" {
			missing = append(missing, id)
		}
	}
	contractcheck.FailViolations(t, "inform-content catalog Anchors missing Binding (excl. loop.wake)", missing)
}

func TestNoHardcodedInjectStems(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	stems := []string{
		`"worker-leg"`,
		`"implement-spawn"`,
		`"blueprint"`,
		`"worker-task-assignment"`,
		`"worker-task-preamble"`,
		`"scan-guidance-ephemeral"`,
		`"agents-md"`,
		`"active-workflow"`,
		`"board-orientation"`,
	}
	var hits []string
	dirs := []string{
		filepath.Join(root, "lycaon", "internal", "coordinator", "inject"),
		filepath.Join(root, "lycaon", "internal", "governance"),
		filepath.Join(root, "lycaon", "internal", "worker"),
		filepath.Join(root, "lycaon", "internal", "scan"),
	}
	for _, dir := range dirs {
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			body := string(src)
			rel, _ := filepath.Rel(root, path)
			for _, stem := range stems {
				if strings.Contains(body, "Render(ctx, "+stem) || strings.Contains(body, "Render(ctx,"+stem) {
					hits = append(hits, rel+": literal stem "+stem)
				}
			}
			return nil
		})
	}
	contractcheck.FailViolations(t, "hardcoded inject stems", hits)
}

func TestNoQueueWorkerKick(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	var hits []string
	fset := token.NewFileSet()
	_ = filepath.Walk(filepath.Join(root, "lycaon", "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if strings.Contains(string(src), "QueueWorkerKick") {
			file, err := parser.ParseFile(fset, path, src, 0)
			if err != nil {
				hits = append(hits, rel+": QueueWorkerKick (parse failed)")
				return nil
			}
			ast.Inspect(file, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if ok && id.Name == "QueueWorkerKick" {
					hits = append(hits, rel+": QueueWorkerKick")
				}
				return true
			})
		}
		return nil
	})
	contractcheck.FailViolations(t, "production QueueWorkerKick", hits)
}
