package contract

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

type authzVocabularyImporter struct{ ledger *types.Package }

func (i authzVocabularyImporter) Import(path string) (*types.Package, error) {
	if path != "github.com/lycaon/lycaon/internal/authzledger" {
		return nil, fmt.Errorf("unexpected vocabulary dependency %q", path)
	}
	return i.ledger, nil
}

func authzVocabulary(t *testing.T, typeName string) []string {
	t.Helper()
	root := contractcheck.RepoRoot(t)
	fset := token.NewFileSet()
	ledger, err := parser.ParseFile(fset, filepath.Join(root, "lycaon/internal/authzledger/recorder.go"), nil, 0)
	contractcheck.FailErr(t, "parse ledger vocabulary", err)
	// Evaluate the declarations without importing runtime recording dependencies.
	var declarations []ast.Decl
	for _, declaration := range ledger.Decls {
		if gen, ok := declaration.(*ast.GenDecl); ok && gen.Tok == token.CONST {
			declarations = append(declarations, gen)
		}
	}
	ledger.Decls, ledger.Imports = declarations, nil
	ledgerPackage, err := new(types.Config).Check("github.com/lycaon/lycaon/internal/authzledger", fset, []*ast.File{ledger}, nil)
	contractcheck.FailErr(t, "evaluate ledger constants", err)
	source, err := parser.ParseFile(fset, filepath.Join(root, "lycaon/internal/authzcontext/event_types.go"), nil, 0)
	contractcheck.FailErr(t, "parse event vocabulary", err)
	config := types.Config{Importer: authzVocabularyImporter{ledger: ledgerPackage}}
	vocabulary, err := config.Check("github.com/lycaon/lycaon/internal/authzcontext", fset, []*ast.File{source}, nil)
	contractcheck.FailErr(t, "evaluate event constants", err)
	var values []string
	seen := map[string]bool{}
	for _, name := range vocabulary.Scope().Names() {
		value, ok := vocabulary.Scope().Lookup(name).(*types.Const)
		if !ok {
			continue
		}
		named, ok := value.Type().(*types.Named)
		if !ok || named.Obj().Name() != typeName {
			continue
		}
		if value.Val().Kind() != constant.String {
			t.Fatalf("%s is not a string constant", name)
		}
		text := constant.StringVal(value.Val())
		if seen[text] {
			t.Fatalf("duplicate %s value %q", typeName, text)
		}
		seen[text] = true
		values = append(values, text)
	}
	if len(values) == 0 {
		t.Fatalf("no %s declarations", typeName)
	}
	return values
}
