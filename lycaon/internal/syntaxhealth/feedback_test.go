package syntaxhealth

import (
	"testing"
)

func TestFeedbackFactsUsePortableValues(t *testing.T) {
	before := Report{Status: StatusBroken, Burden: Burden{SpanBytes: 4, Faults: 2}}
	change := Change{Transition: TransitionRepairRegressed, Before: &before, After: Report{
		Status: StatusBroken, Language: "go", Burden: Burden{SpanBytes: 5, Faults: 3},
		Diagnostics: []Diagnostic{{Row: 2, Col: 3, Kind: DiagnosticMissing, NodeType: "identifier"}, {Row: 4, Kind: DiagnosticError}},
	}}
	facts := change.FeedbackFacts("candidate")
	assertPortableFact(t, facts)
	if facts["syntax_transition"] != string(change.Transition) || facts["before_parse_status"] != "broken" {
		t.Fatalf("lost transition: %#v", facts)
	}
	diagnostics := facts["syntax_diagnostics"].([]any)
	if len(diagnostics) != 2 || diagnostics[0].(map[string]any)["node_type"] != "identifier" {
		t.Fatalf("lost diagnostics: %#v", diagnostics)
	}
	if _, ok := diagnostics[1].(map[string]any)["node_type"]; ok {
		t.Fatal("empty node type was emitted")
	}
	if facts["syntax_diagnostic"].(map[string]any)["row"] != 2 || facts["after_syntax_burden"].(map[string]any)["faults"] != 3 {
		t.Fatalf("lost location or burden: %#v", facts)
	}
}

func assertPortableFact(t *testing.T, value any) {
	t.Helper()
	switch v := value.(type) {
	case nil, string, bool, int, int64, float64, []string:
	case map[string]any:
		for _, item := range v {
			assertPortableFact(t, item)
		}
	case []any:
		for _, item := range v {
			assertPortableFact(t, item)
		}
	default:
		t.Fatalf("non-portable feedback value %T", value)
	}
}
