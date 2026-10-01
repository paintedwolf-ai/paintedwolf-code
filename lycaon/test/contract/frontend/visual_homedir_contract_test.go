package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestVisualStoreNoHomedirContract(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	dir := filepath.Join(root, "lycaon", "internal", "visual")
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if pkg.Name == "os" && sel.Sel.Name == "UserHomeDir" {
				t.Errorf("%s: os.UserHomeDir forbidden — resolve via project.HostSubdir/HostDataDir", path)
			}
			return true
		})
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(body)
		for _, needle := range []string{
			`filepath.Join(home`,
			`filepath.Join(homedir`,
			`"/.paintedwolf"`,
			`'.paintedwolf'`,
			`~/.paintedwolf`,
			`"/.paintedwolf-dev"`,
			`'.paintedwolf-dev'`,
			`~/.paintedwolf-dev`,
			`"/.lycaon"`,
			`'.lycaon'`,
			`~/.lycaon`,
			`"/.paintedwolfcode"`,
			`'.paintedwolfcode'`,
			`~/.paintedwolfcode`,
		} {
			if strings.Contains(text, needle) {
				t.Errorf("%s: hardcoded home/overlay path %q — inject ArtifactsDir only", path, needle)
			}
		}
		return nil
	})
	testutil.FailErr(t, "walk visual", err)
}
