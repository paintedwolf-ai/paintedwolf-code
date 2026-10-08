package promptloop

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/argdiag"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

func TestToolSchemaOrderIsStableAcrossCatalogEnumeration(t *testing.T) {
	a := tools.ToolMeta{Name: "read", ArgsSchema: map[string]any{"type": "object"}}
	b := tools.ToolMeta{Name: "command", ArgsSchema: map[string]any{"type": "object"}}
	plan := toolsurface.Compile([]string{"read", "command"}, nil)
	for _, project := range []func([]tools.ToolMeta) []tools.ToolMeta{
		trimPromptToolMetas,
		func(metas []tools.ToolMeta) []tools.ToolMeta {
			return trimCoordinatorToolMetasForPlan(plan, metas, nil, nil)
		},
	} {
		first, err := json.Marshal(project([]tools.ToolMeta{a, b}))
		if err != nil {
			t.Fatalf("marshal tool schemas: %v", err)
		}
		second, err := json.Marshal(project([]tools.ToolMeta{b, a}))
		if err != nil {
			t.Fatalf("marshal tool schemas: %v", err)
		}
		if string(first) != string(second) {
			t.Fatalf("schema order depends on catalog enumeration: %s != %s", first, second)
		}
	}
}

func TestReviewPhaseOffersItsVerdictSchema(t *testing.T) {
	catalog := map[string]any{"type": "object", "properties": map[string]any{"verdict": map[string]any{"type": "object"}}}
	phase := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{"verdict": map[string]any{
			"type":     "object",
			"required": []any{"verdict"},
			"properties": map[string]any{
				"verdict": map[string]any{"type": "string", "enum": []any{"CLAIMED"}},
			},
		}},
	}
	metas := []tools.ToolMeta{{Name: "submit_verdict", ArgsSchema: catalog}, {Name: "read", ArgsSchema: map[string]any{"type": "object"}}}
	frame := inject.CoordinatorTurnFrame{Runtime: inject.WorkflowRuntimeSnapshot{PhaseExit: &inject.PhaseExitView{SubmitVerdictArgsSchema: phase}}}

	offered := trimCoordinatorToolMetasForPlan(toolsurface.Compile([]string{"submit_verdict", "read"}, nil), metas, nil, phaseVerdictSchema(frame))
	var verdict tools.ToolMeta
	for _, meta := range offered {
		if meta.Name == "submit_verdict" {
			verdict = meta
		}
	}
	props, _ := verdict.ArgsSchema["properties"].(map[string]any)
	member, _ := props["verdict"].(map[string]any)
	if got := argdiag.Outline(member); got != "{verdict: CLAIMED}" {
		t.Fatalf("offered verdict member = %s, want the phase schema", got)
	}
	if _, ok := metas[0].ArgsSchema["additionalProperties"]; ok {
		t.Fatal("offering the phase schema mutated the catalog meta")
	}
	if outside := phaseVerdictSchema(inject.CoordinatorTurnFrame{}); outside != nil {
		t.Fatal("a turn outside a review phase got a phase verdict schema")
	}
}
