package oar

import (
	"testing"
)

func TestFactCatalogueMatchesConditionEnvironment(t *testing.T) {
	for _, f := range FactCatalogue() {
		var expr string
		switch f.Type {
		case "bool":
			expr = PublishedFactName(f.Name)
		case "string":
			expr = PublishedFactName(f.Name) + ` == ""`
		case "int":
			expr = PublishedFactName(f.Name) + " >= 0"
		case "double":
			expr = PublishedFactName(f.Name) + " >= 0.0"
		case "list<string>":
			expr = "size(" + PublishedFactName(f.Name) + ") >= 0"
		case "map", "map<string,string>", "list<map>":
			expr = "size(" + PublishedFactName(f.Name) + ") >= 0"
		default:
			t.Fatalf("fact %q has unhandled docs type %q", f.Name, f.Type)
		}
		if err := checkWhenAgainstSpec(expr, nil); err != nil {
			t.Fatalf("catalogued fact %q is unavailable: %v", f.Name, err)
		}
	}
	for _, fn := range ObservationFunctionCatalogue() {
		var expr string
		switch fn.Signature {
		case "(string) -> bool":
			expr = PublishedFactName(fn.Name) + `("x")`
		case "(string) -> string":
			expr = PublishedFactName(fn.Name) + `("x") == ""`
		case "(string) -> int":
			expr = PublishedFactName(fn.Name) + `("x") >= 0`
		default:
			t.Fatalf("function %q has unhandled signature %q", fn.Name, fn.Signature)
		}
		if err := checkWhenAgainstSpec(expr, nil); err != nil {
			t.Fatalf("catalogued function %q is unavailable: %v", fn.Name, err)
		}
	}
}

// TestFactCatalogue_Classes pins the derived class taxonomy to the closed set
// the docs render, and spot-checks each derivation source.
func TestFactCatalogue_Classes(t *testing.T) {
	valid := map[string]bool{"structural": true, "derived": true, "engine_counter": true, "detector": true}
	byName := map[string]FactInfo{}
	for _, f := range FactCatalogue() {
		if !valid[f.Class] {
			t.Fatalf("fact %q has unknown class %q", f.Name, f.Class)
		}
		byName[f.Name] = f
	}
	for name, want := range map[string]string{
		"tool":              "structural",
		"claims_completion": "derived",
		"repeat_count":      "engine_counter",
		"secret_matches":    "detector",
	} {
		if got := byName[name].Class; got != want {
			t.Fatalf("fact %q class = %q want %q", name, got, want)
		}
	}
}

// TestFactCatalogue_TypesRendered pins the documented fact types.
func TestFactCatalogue_TypesRendered(t *testing.T) {
	want := map[string]string{
		"tool":           "string",
		"workers_idle":   "bool",
		"summary_length": "int",
		"secret_matches": "list<map>",
		"write_roots":    "list<string>",
		"tool_args":      "map",
	}
	for _, f := range FactCatalogue() {
		if w, ok := want[f.Name]; ok && f.Type != w {
			t.Fatalf("fact %q type = %q want %q", f.Name, f.Type, w)
		}
	}
}

// TestFactCatalogue_LazyAssemblyRecognizesEnvFacts asserts the lazy-assembly
// name set (facts.go catalogueFacts) covers every fact a rule can reference,
// so FactsReferenced cannot silently drop an env-declared fact.
func TestFactCatalogue_LazyAssemblyRecognizesEnvFacts(t *testing.T) {
	var missing []string
	for _, d := range factDecls {
		if _, ok := catalogueFacts[publishedName(d.name, d.tier)]; !ok {
			missing = append(missing, d.name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("env facts missing from facts.go catalogueFacts: %v", missing)
	}
}
