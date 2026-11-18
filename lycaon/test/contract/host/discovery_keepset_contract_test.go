package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Discovery strategies without a compatible /models endpoint stay documented.
func TestDiscoveryStrategyConstsInKeepSet(t *testing.T) {
	t.Parallel()
	path := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "llm", "providerprofile", "profile.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	contractcheck.FailErr(t, "parse providerprofile/profile.go", err)

	consts := discoveryStrategyConsts(f)
	if len(consts) == 0 {
		t.Fatal("no DiscoveryStrategy consts found in providerprofile/profile.go")
	}
	for name, value := range consts {
		gap, ok := llm.DiscoveryKeepSetGap(providerprofile.DiscoveryStrategy(value))
		if !ok {
			t.Errorf("%s (%q) missing from discoveryKeepSet — document the feature gap before minting", name, value)
			continue
		}
		if strings.TrimSpace(gap) == "" {
			t.Errorf("%s (%q) has empty keep-set gap text", name, value)
		}
	}
}

func discoveryStrategyConsts(f *ast.File) map[string]string {
	out := make(map[string]string)
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		var iotaType string
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			typeName := ""
			if vs.Type != nil {
				if ident, ok := vs.Type.(*ast.Ident); ok {
					typeName = ident.Name
					iotaType = typeName
				}
			} else if iotaType != "" {
				typeName = iotaType
			}
			if typeName != "DiscoveryStrategy" {
				if vs.Type != nil {
					iotaType = ""
				}
				continue
			}
			for i, name := range vs.Names {
				if name.Name == "_" || !strings.HasPrefix(name.Name, "Discovery") {
					continue
				}
				if len(vs.Values) <= i {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				out[name.Name] = strings.Trim(lit.Value, `"`)
			}
		}
	}
	return out
}
