package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestSurveyWalksUseSharedPrimitive: filesystem-survey surfaces walk project trees
// only through sandbox.SurveyWalk, the one place the prune policy applies (engine
// and VCS metadata only, not .gitignore). Raw filepath.WalkDir / fs.WalkDir fail.
func TestSurveyWalksUseSharedPrimitive(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	const primitive = "internal/sandbox/survey_walk.go" // the one implementation
	var violations []string
	for _, sub := range []string{
		"internal/tools/native",
		"internal/repomap",
		"internal/workspace",
		"internal/sandbox",
	} {
		base := filepath.Join(root, "lycaon", sub)
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			if strings.HasSuffix(relRepoPath(root, path), primitive) {
				return nil
			}
			fset := token.NewFileSet()
			file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if perr != nil {
				return perr
			}
			fpAlias := contractcheck.ImportAliasFor(file, "path/filepath")
			fsAlias := contractcheck.ImportAliasFor(file, "io/fs")
			if fpAlias == "" && fsAlias == "" {
				return nil
			}
			ast.Inspect(file, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "WalkDir" {
					return true
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				if (fpAlias != "" && ident.Name == fpAlias) || (fsAlias != "" && ident.Name == fsAlias) {
					violations = append(violations, relRepoPath(root, path))
				}
				return true
			})
			return nil
		})
		testutil.FailErr(t, "walk "+sub, err)
	}
	if len(violations) > 0 {
		contractcheck.FailViolations(t, "raw filepath.WalkDir/fs.WalkDir is forbidden in survey packages — route project-tree traversal through sandbox.SurveyWalk (the single skip-policy SSOT)", violations)
	}
}
