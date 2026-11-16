package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

func TestValidateRejectsLabelWithSurroundingWhitespace(t *testing.T) {
	cfg, schemas := loadBundled(t)
	read := cfg.Tools["read"]
	read.Label = " Read file"
	cfg.Tools["read"] = read
	err := cfg.Validate(schemas)
	if err == nil || !strings.Contains(err.Error(), `tool "read": label " Read file" has surrounding whitespace`) {
		t.Fatalf("expected a padded label to fail validation, got: %v", err)
	}
}

func TestLabelsCoverEveryToolExceptWorkerCards(t *testing.T) {
	cfg, _ := loadBundled(t)
	labels := cfg.Labels()
	for name, entry := range cfg.Tools {
		_, labeled := labels[name]
		if worker := entry.Kind == KindTask; worker == labeled {
			t.Fatalf("tool %q: worker=%v labeled=%v", name, worker, labeled)
		}
	}
}

// The generated Go answers Label with exactly the catalog's labels.
func TestRenderedGoCarriesEveryLabel(t *testing.T) {
	cfg, _ := loadBundled(t)
	file, err := parser.ParseFile(token.NewFileSet(), "visibility.generated.go", renderGo(cfg), 0)
	if err != nil {
		t.Fatalf("rendered Go does not parse: %v", err)
	}
	rendered := map[string]string{}
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || spec.Names[0].Name != "actionLabels" {
			return true
		}
		for _, element := range spec.Values[0].(*ast.CompositeLit).Elts {
			pair := element.(*ast.KeyValueExpr)
			key, _ := strconv.Unquote(pair.Key.(*ast.BasicLit).Value)
			value, _ := strconv.Unquote(pair.Value.(*ast.BasicLit).Value)
			rendered[key] = value
		}
		return false
	})
	want := cfg.Labels()
	if len(rendered) != len(want) {
		t.Fatalf("rendered %d labels, catalog has %d", len(rendered), len(want))
	}
	for name, label := range want {
		if rendered[name] != label {
			t.Fatalf("tool %q: rendered %q, catalog %q", name, rendered[name], label)
		}
	}
}
