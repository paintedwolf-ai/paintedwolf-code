package oar

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
)

func testRemedyGuardContext(data map[string]any) *GuardContext {
	gc := &GuardContext{ObservationData: data}
	gc.Invocation.Tool = "read"
	return gc
}

func TestResolveRemediesBindsArgsAndPicksOneEntry(t *testing.T) {
	rule := &Rule{ID: "R", Remedies: []guidance.RemedyEntry{{
		Tool: "request_tools",
		Args: map[string]string{"need": "{{ tool }}"},
	}, {
		Pick:    map[string]string{"capability_request.read_path": "paintedwolf.refused_read_grants"},
		Outcome: guidance.RemedyObserves,
	}}}
	got, err := ResolveRemedies(rule, testRemedyGuardContext(map[string]any{
		"refused_read_grants": []string{"/supplied/a", "/supplied/b"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := []ResolvedRemedy{
		{Tool: "request_tools", Args: map[string]any{"need": "read"}, Outcome: guidance.RemedyClears},
		{Args: map[string]any{"capability_request": map[string]any{"read_path": "/supplied/a"}}, Outcome: guidance.RemedyObserves},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolved = %#v", got)
	}
}

func TestResolveRemediesReturnsNothingForAPortableRule(t *testing.T) {
	got, err := ResolveRemedies(&Rule{ID: "R"}, testRemedyGuardContext(nil))
	if err != nil || got != nil {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestRemedyFactRefsStayInsideTheRule(t *testing.T) {
	loader, err := NewLoader("")
	if err != nil {
		t.Fatal(err)
	}
	rule := &Rule{ID: "R", Requires: Requires{Facts: []string{"paintedwolf.tool_loadable"}}}
	rule.Remedies = []guidance.RemedyEntry{{When: "paintedwolf.tool_loadable", Tool: "request_tools"}}
	if err := loader.validateRemedies(rule); err != nil {
		t.Fatalf("declared fact refused: %v", err)
	}
	rule.Remedies = []guidance.RemedyEntry{{Pick: map[string]string{"a": "paintedwolf.refused_read_grants"}}}
	if err := loader.validateRemedies(rule); err == nil {
		t.Fatal("a fact the rule does not reference was accepted")
	}
	rule.Remedies = []guidance.RemedyEntry{{Outcome: "fixes"}}
	if err := loader.validateRemedies(rule); err == nil {
		t.Fatal("an unknown outcome was accepted")
	}
}
