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
)

const recoveryComposeRoot = "../../../internal/app"

func walkComposeRoot(t *testing.T, fset *token.FileSet, visit func(string, *ast.File)) {
	t.Helper()
	err := filepath.WalkDir(recoveryComposeRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		visit(path, file)
		return nil
	})
	testutil.FailErr(t, "walk app composition owners", err)
}

func isRecoveryEntryLit(lit *ast.CompositeLit) bool {
	sel, ok := lit.Type.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "bootrecovery" && sel.Sel.Name == "Entry"
}

func lineOf(fset *token.FileSet, pos token.Pos) string {
	return strings.TrimPrefix(fset.Position(pos).String(), fset.Position(pos).Filename+":")
}
