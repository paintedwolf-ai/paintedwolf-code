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

func TestNoDeleteFromMessagesInLifecycleSQL(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	packages := []string{
		filepath.Join(root, "lycaon", "internal", "session"),
		filepath.Join(root, "lycaon", "internal", "coordinator", "promptloop"),
	}
	for _, pkgDir := range packages {
		err := filepath.WalkDir(pkgDir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, body, 0)
			if err != nil {
				return err
			}
			for _, declaration := range file.Decls {
				method, ok := declaration.(*ast.FuncDecl)
				if ok && method.Recv != nil && method.Name.Name == "DeleteMessage" {
					t.Errorf("%s: session lifecycle must not define DeleteMessage", path)
				}
			}
			if strings.Contains(string(body), "DELETE FROM messages") {
				t.Errorf("%s: lifecycle path must not DELETE FROM messages", path)
			}
			return nil
		})
		testutil.FailErr(t, "walk "+pkgDir, err)
	}
}
