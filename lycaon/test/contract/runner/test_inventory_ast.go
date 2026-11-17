package contract

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func walkTestFiles(t *testing.T, root string, visit func(pkg string, fset *token.FileSet, file *ast.File, src []byte)) {
	t.Helper()
	lycaonRoot := filepath.Join(root, "lycaon")
	corpus, err := contractcheck.LoadGoASTCorpus(lycaonRoot)
	contractcheck.FailErr(t, "load lycaon Go corpus", err)
	for _, file := range corpus.Files() {
		if !file.IsTest {
			continue
		}
		pkg := "./" + filepath.ToSlash(filepath.Dir(file.Rel))
		visit(pkg, corpus.Fset, file.AST, file.Bytes())
	}
}
