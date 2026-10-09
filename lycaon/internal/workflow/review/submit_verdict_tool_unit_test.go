package review

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func TestParseSubmitVerdictArgsUsesDeclaredTypes(t *testing.T) {
	def := workflowdef.ReviewLoopDef{VerdictSchema: map[string]string{"verdict": "ADJUDICATED", "vulnerabilities": workflowdef.VerdictClaimsType, "dismissed": workflowdef.VerdictSetAsidesType}}
	verdict, cited, citedURLs, err := parseSubmitVerdictArgs(def, map[string]any{
		"verdict": map[string]any{
			"verdict":         "ADJUDICATED",
			"vulnerabilities": []any{},
			"dismissed":       []any{map[string]any{"issue": "x"}},
		},
		"cited_evidence": []any{
			map[string]any{"path": "go.mod", "line": float64(1), "excerpt": "module x"},
		},
		"cited_urls": []any{" https://example.com/advisory "},
	})
	testutil.FailErr(t, "parseSubmitVerdictArgs", err)
	if verdict["verdict"] != "ADJUDICATED" {
		t.Fatalf("verdict = %q", verdict["verdict"])
	}
	if verdict["vulnerabilities"] != "[]" {
		t.Fatalf("vulnerabilities = %q want stored JSON array", verdict["vulnerabilities"])
	}
	if verdict["dismissed"] != `[{"issue":"x"}]` {
		t.Fatalf("dismissed = %q", verdict["dismissed"])
	}
	if len(cited) != 1 || cited[0].Path != "go.mod" || cited[0].Line != 1 {
		t.Fatalf("cited = %+v", cited)
	}
	if len(citedURLs) != 1 || citedURLs[0] != "https://example.com/advisory" {
		t.Fatalf("citedURLs = %+v", citedURLs)
	}

	if _, _, _, err := parseSubmitVerdictArgs(def, map[string]any{}); err == nil {
		t.Fatal("expected error for missing verdict object")
	}
	if _, _, _, err := parseSubmitVerdictArgs(def, map[string]any{
		"verdict":        map[string]any{"verdict": "X"},
		"cited_evidence": []any{map[string]any{"line": float64(3)}},
	}); err == nil {
		t.Fatal("expected error for cited_evidence without handle or path")
	}
	for name, args := range map[string]map[string]any{
		"unknown top-level field": {
			"verdict": map[string]any{"verdict": "X"}, "claims": []any{},
		},
		"evidence key instead of handle": {
			"verdict":        map[string]any{"verdict": "X"},
			"cited_evidence": []any{map[string]any{"evidence": "reviewer:read#1"}},
		},
		"invalid URL item": {
			"verdict": map[string]any{"verdict": "X"}, "cited_urls": []any{7},
		},
		"URL without host": {
			"verdict": map[string]any{"verdict": "X"}, "cited_urls": []any{"https://"},
		},
		"handle with path fields": {
			"verdict":        map[string]any{"verdict": "X"},
			"cited_evidence": []any{map[string]any{"handle": "reviewer:read#1", "line": float64(4)}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := parseSubmitVerdictArgs(def, args); err == nil {
				t.Fatalf("parseSubmitVerdictArgs(%s) unexpectedly succeeded", name)
			}
		})
	}
}
func TestVerdictFieldTypesRejectCoercion(t *testing.T) {
	def := workflowdef.ReviewLoopDef{VerdictSchema: map[string]string{"verdict": "ACCEPTED", "summary": "string", "claims": workflowdef.VerdictClaimsType, "coverage": workflowdef.VerdictCoverageType}}
	for _, tc := range []struct {
		field string
		value any
	}{
		{"summary", []any{"claim"}}, {"summary", 1}, {"summary", false}, {"summary", nil},
		{"claims", "[]"}, {"claims", map[string]any{}}, {"claims", nil},
		{"coverage", "{}"}, {"coverage", []any{}}, {"coverage", nil},
	} {
		_, _, _, err := parseSubmitVerdictArgs(def, map[string]any{"verdict": map[string]any{"verdict": "ACCEPTED", tc.field: tc.value}})
		if err == nil {
			t.Errorf("field %s accepted %T", tc.field, tc.value)
		}
	}
}
