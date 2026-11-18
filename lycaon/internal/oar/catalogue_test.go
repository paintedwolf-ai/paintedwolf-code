package oar

import (
	"regexp"
	"testing"

	"github.com/lycaon/lycaon/internal/oarcopy"
)

var engineCounterFacts = map[string]struct{}{
	"fire_count": {}, "breaker_count": {}, "same_code_reject_run": {}, "same_call_run": {},
	"paintedwolf.repeat_count": {}, "paintedwolf.fruitless_search_run": {},
	"paintedwolf.code_reject_responses": {}, "paintedwolf.credential_decisions": {},
}

var detectorFacts = map[string]struct{}{
	"secret_matches": {},
}

var expensiveFacts = map[string]struct{}{
	"paintedwolf.unobserved_cited_paths": {}, "paintedwolf.unobserved_cited_urls": {},
	"paintedwolf.unobserved_cited_handles": {},
	"paintedwolf.claims_completion":        {}, "paintedwolf.has_matching_ledger_job": {},
	"paintedwolf.worker_summary_present":  {},
	"paintedwolf.worker_artifact_present": {},
	"paintedwolf.worktree_stale_paths":    {}, "paintedwolf.worktree_leftover_paths": {},
	"paintedwolf.worktree_conflict_paths":      {},
	"paintedwolf.reproduced_source_lines":      {},
	"paintedwolf.reproduced_source_file_lines": {},
	"paintedwolf.reproduced_source_path":       {},
	"paintedwolf.reproduced_source_start_line": {},
	"paintedwolf.reproduced_source_end_line":   {},
}

type FactInfo struct {
	Name          string
	Type          string
	Class         string
	Tier          FactTier
	Profile       string
	PublishedName string
}

type FunctionInfo struct {
	Name          string
	Signature     string
	Tier          FactTier
	Profile       string
	PublishedName string
}

func FactCatalogue() []FactInfo {
	out := make([]FactInfo, 0, len(factDecls))
	for _, d := range factDecls {
		out = append(out, FactInfo{
			Name:          d.name,
			Type:          string(d.typ),
			Class:         factClass(d.name),
			Tier:          d.tier,
			Profile:       d.profile,
			PublishedName: publishedHostName(d.name, d.tier),
		})
	}
	return out
}

func ObservationFunctionCatalogue() []FunctionInfo {
	out := make([]FunctionInfo, 0, len(observationFns))
	for _, f := range observationFns {
		out = append(out, FunctionInfo{
			Name:          f.name,
			Signature:     "(string) -> " + string(f.ret),
			Tier:          f.tier,
			Profile:       f.profile,
			PublishedName: publishedHostName(f.name, f.tier),
		})
	}
	return out
}

func factClass(name string) string {
	name = PublishedFactName(name)
	if _, ok := engineCounterFacts[name]; ok {
		return "engine_counter"
	}
	if _, ok := detectorFacts[name]; ok {
		return "detector"
	}
	if _, ok := expensiveFacts[name]; ok {
		return "derived"
	}
	return "structural"
}

func publishedHostName(name string, tier FactTier) string {
	if tier != FactTierHost {
		return ""
	}
	return oarcopy.HostFactNamespace + "." + name
}

func PublishedFactName(name string) string {
	if published := publishedHostName(name, bareFactTiers[name]); published != "" {
		return published
	}
	return name
}

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
		case "map<string, dyn>", "list<map<string, dyn>>":
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
		"secret_matches": "list<map<string, dyn>>",
		"tool_args":      "map<string, dyn>",
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

func TestFactCatalogue_PortableFactsMatchOARCopy(t *testing.T) {
	eachDeclaredFact(func(d factDecl) {
		wantPortable := d.tier != FactTierHost
		if got := oarcopy.IsPortableFact(d.name); got != wantPortable {
			t.Errorf("fact %q (tier %q): oarcopy.IsPortableFact = %v, want %v", d.name, d.tier, got, wantPortable)
		}
	})
}

// Facts are observations; the verdict belongs to the rule that reads them.
var verdictFactName = regexp.MustCompile(`(?i)(_violation|_is_ungrounded|_verdict|_decision_code|reject_code)$`)

func TestFactCatalogueHoldsNoVerdictFacts(t *testing.T) {
	facts := FactCatalogue()
	if len(facts) == 0 {
		t.Fatal("fact catalogue is empty")
	}
	for _, f := range facts {
		if verdictFactName.MatchString(f.Name) {
			t.Errorf("catalogue declares verdict-shaped fact %q", f.Name)
		}
	}
}
