package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestInformBindingsCoverKickTemplates closes bindings over bundled templates.
func TestInformBindingsCoverKickTemplates(t *testing.T) {
	t.Parallel()
	catalogfixture.AssertNoKickConstFiles(t)
	reg := catalogfixture.LoadInformBindings(t)

	var missing []string
	for _, b := range reg.AllInform() {
		if b == nil || !b.IsInform() {
			continue
		}
		if _, err := catalogfixture.StockGuidancePath(b.Render); err != nil {
			missing = append(missing, string(b.On)+" → missing "+b.Render+".md (stock guidance/)")
		}
	}
	contractcheck.FailViolations(t, "inform Bindings missing kick templates", missing)
}

// TestNoKickConstOrCoordinatorConcatDispatch enforces registry-based dispatch.
func TestNoKickConstOrCoordinatorConcatDispatch(t *testing.T) {
	t.Parallel()
	catalogfixture.AssertNoKickConstFiles(t)
	root := contractcheck.RepoRoot(t)
	fset := token.NewFileSet()
	var hits []string
	walk := func(dir string) {
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			body := string(src)
			if strings.Contains(body, "KickGateBlocked") || strings.Contains(body, "KickLegFinished") ||
				strings.Contains(body, "KickWorkerCloseout") || strings.Contains(body, `KickFeedbackReceived =`) {
				rel, _ := filepath.Rel(root, path)
				hits = append(hits, rel+": Kick* const residue")
			}
			file, err := parser.ParseFile(fset, path, src, 0)
			if err != nil {
				return nil
			}
			ast.Inspect(file, func(n ast.Node) bool {
				bin, ok := n.(*ast.BinaryExpr)
				if !ok || bin.Op != token.ADD {
					return true
				}
				lit, ok := bin.X.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				if lit.Value == `"coordinator-"` {
					rel, _ := filepath.Rel(root, path)
					hits = append(hits, rel+`: "coordinator-"+ dispatch`)
				}
				return true
			})
			return nil
		})
	}
	walk(filepath.Join(root, "lycaon", "internal", "coordinator"))
	walk(filepath.Join(root, "lycaon", "internal", "session"))
	contractcheck.FailViolations(t, "non-registry coordinator dispatch", hits)
}
