package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// The matcher names its evidence lens and the wire republishes it through a
// string cast, so a lens added on one side and not the other reaches Den as a
// value its enum does not contain.
func TestRedactionSourceMatchesTheWireEnum(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	wire := map[string]bool{
		string(api.RedactionSourceShapeRule):        true,
		string(api.RedactionSourceContainerHarvest): true,
		string(api.RedactionSourceRememberedMatch):  true,
		// policy has no matcher counterpart: an observer mask detects nothing.
		string(api.RedactionSourcePolicy): true,
	}
	values := typedStringConsts(t, filepath.Join(root, "lycaon/internal/secretmatch/redaction.go"), "RedactionSource")
	if len(values) == 0 {
		t.Fatal("no secretmatch.RedactionSource constants found")
	}
	for name, value := range values {
		if !wire[value] {
			t.Errorf("secretmatch.%s = %q has no api.RedactionSource; the cast would emit an unknown wire value", name, value)
		}
	}
}

// typedStringConsts returns name→value for every const of one string type.
func typedStringConsts(t *testing.T, path, typeName string) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	testutil.FailErr(t, "parse "+path, err)
	out := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if ident, ok := value.Type.(*ast.Ident); !ok || ident.Name != typeName {
				continue
			}
			for i, name := range value.Names {
				if i < len(value.Values) {
					if lit, ok := value.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						out[name.Name] = strings.Trim(lit.Value, `"`)
					}
				}
			}
		}
	}
	return out
}
