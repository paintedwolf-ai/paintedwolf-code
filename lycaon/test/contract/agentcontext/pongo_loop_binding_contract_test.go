package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/pongoplain"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Pongo silently resolves unknown struct fields to nil. Discover its actual
// loop ABI from the pinned dependency instead of copying the member list.
func TestCatalogLoopBindingsExistInPongo(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(pongoModuleDir(t), "tags_for.go"), nil, 0)
	contractcheck.FailErr(t, "parse Pongo loop implementation", err)
	fields := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.TypeSpec)
		if !ok || spec.Name.Name != "tagForLoopInformation" {
			return true
		}
		for _, field := range spec.Type.(*ast.StructType).Fields.List {
			nested := false
			if ptr, ok := field.Type.(*ast.StarExpr); ok {
				if ident, ok := ptr.X.(*ast.Ident); ok {
					nested = ident.Name == spec.Name.Name
				}
			}
			for _, name := range field.Names {
				fields[name.Name] = nested
			}
		}
		return false
	})
	if len(fields) == 0 {
		t.Fatal("Pongo loop ABI not found")
	}
	checked := 0
	err = filepath.WalkDir(filepath.Join(contractcheck.RepoRoot(t), "lycaon/config/packs"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		switch filepath.Ext(path) {
		case ".md", ".yaml", ".tmpl":
		default:
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, binding := range pongoplain.LoopBindings(string(body)) {
			checked++
			if !validLoopBinding(binding, fields) {
				t.Errorf("%s: unknown Pongo loop member in %s", path, binding)
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "scan catalog template bindings", err)
	if checked == 0 {
		t.Fatal("no loop bindings exercised")
	}
}

func validLoopBinding(binding string, fields map[string]bool) bool {
	members := strings.Split(binding, ".")[1:]
	for i, member := range members {
		nested, exists := fields[member]
		if !exists || (i < len(members)-1 && !nested) {
			return false
		}
	}
	return len(members) > 0
}

func TestLoopBindingDiscoveryIgnoresLiteralCopy(t *testing.T) {
	source := `forloop.prose {# {{ forloop.comment }} #} {% comment %}{{ forloop.hidden }}{% endcomment %}
{% verbatim %}{{ forloop.literal }}{% endverbatim %}
{{ "forloop.string" }} {% if not forloop.last %}x{% endif %} {{ forloop.Parentloop.Counter }}`
	got := pongoplain.LoopBindings(source)
	if len(got) != 2 || got[0] != "forloop.last" || got[1] != "forloop.Parentloop.Counter" {
		t.Fatalf("bindings = %v", got)
	}
}

func TestLoopBindingRuleRejectsUnknownAndScalarTraversal(t *testing.T) {
	fields := map[string]bool{"Last": false, "Parentloop": true}
	for binding, valid := range map[string]bool{
		"forloop.Last": true, "forloop.Parentloop.Last": true,
		"forloop.last": false, "forloop.Last.Last": false, "forloop.Parentloop.missing": false,
	} {
		if got := validLoopBinding(binding, fields); got != valid {
			t.Errorf("binding %s valid=%v", binding, got)
		}
	}
}
