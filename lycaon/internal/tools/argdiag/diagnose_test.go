package argdiag_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
)

// verdictCallSchema is a closed call shape with a nested object member, the
// structure a review phase publishes for submit_verdict.
func verdictCallSchema() map[string]any {
	str := map[string]any{"type": "string", "minLength": 1}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"verdict"},
		"properties": map[string]any{
			"cited_evidence": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"verdict": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"verdict", "claims", "coverage", "set_asides", "threat_model"},
				"properties": map[string]any{
					"verdict": map[string]any{"type": "string", "enum": []any{"CLAIMED"}},
					"claims": map[string]any{"type": "array", "items": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"required":             []any{"id", "statement"},
						"properties":           map[string]any{"id": str, "statement": str},
					}},
					"coverage": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"required":             []any{"revision", "assessments"},
						"properties": map[string]any{
							"revision":    str,
							"assessments": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
						},
					},
					"set_asides":   map[string]any{"type": "array"},
					"threat_model": str,
				},
			},
		},
	}
}

// unclosedCoverageText writes coverage without its closing brace, so the
// members after it are read into coverage and the outer object never closes.
func unclosedCoverageText(claims int) string {
	parts := make([]string, claims)
	for i := range parts {
		parts[i] = fmt.Sprintf(`{"id": "c%d", "statement": "Same-user processes can read the token file and call every protected route."}`, i)
	}
	return `{"claims": [` + strings.Join(parts, ", ") + `], "coverage": {"assessments": [], "revision": "r1", ` +
		`"set_asides": [], "threat_model": "Local agent on loopback.", "verdict": "CLAIMED"}`
}

func assertMisplaced(t *testing.T, data map[string]any, fields []string, found, belongs string) {
	t.Helper()
	got, _ := data["misplaced_fields"].([]string)
	if !slices.Equal(got, fields) || data["found_under"] != found || data["belongs_under"] != belongs {
		t.Fatalf("misplaced = %v under %q → %q, want %v under %q → %q",
			got, data["found_under"], data["belongs_under"], fields, found, belongs)
	}
}

func rejectArgs(t *testing.T, schema, args map[string]any) map[string]any {
	t.Helper()
	reject := tools.ValidateCallArguments("submit_verdict", args, schema, tools.ToolContext{})
	if reject == nil || reject.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("expected TOOL_ARGS_INVALID, got %#v", reject)
	}
	return reject.Data
}

func TestDiagnoseMalformedJSONTextNamesTheUnclosedObject(t *testing.T) {
	data := rejectArgs(t, verdictCallSchema(), map[string]any{"verdict": unclosedCoverageText(1)})

	if data["field"] != "verdict" || data["json_malformed"] != true || data["expected_type"] != "object" {
		t.Fatalf("field = %v json_malformed = %v expected_type = %v", data["field"], data["json_malformed"], data["expected_type"])
	}
	if open, _ := data["json_open_paths"].([]string); !slices.Equal(open, []string{"verdict"}) {
		t.Fatalf("json_open_paths = %v, want [verdict]", open)
	}
	assertMisplaced(t, data, []string{"set_asides", "threat_model", "verdict"}, "verdict.coverage", "verdict")
	if data["close_before"] != "set_asides" {
		t.Fatalf("close_before = %v, want set_asides", data["close_before"])
	}

	rep, _ := data["replacement_args"].(map[string]any)
	verdict, _ := rep["verdict"].(map[string]any)
	coverage, _ := verdict["coverage"].(map[string]any)
	if verdict["verdict"] != "CLAIMED" || verdict["threat_model"] == nil || len(coverage) != 2 {
		t.Fatalf("replacement verdict = %#v", verdict)
	}
	if tools.ValidateToolArgs(verdictCallSchema(), rep) != nil {
		t.Fatal("replacement_args does not validate")
	}
}

func TestDiagnoseMalformedTextPositionIsOneBased(t *testing.T) {
	data := rejectArgs(t, verdictCallSchema(), map[string]any{"verdict": `{"claims": [}`})
	if data["json_unexpected_token_at"] != 13 {
		t.Fatalf("json_unexpected_token_at = %v, want 13", data["json_unexpected_token_at"])
	}
}

func TestDiagnoseLargeMalformedTextOmitsReplacement(t *testing.T) {
	data := rejectArgs(t, verdictCallSchema(), map[string]any{"verdict": unclosedCoverageText(40)})
	if _, ok := data["replacement_args_json"]; ok {
		t.Fatal("a repair larger than the echo bound was offered")
	}
	assertMisplaced(t, data, []string{"set_asides", "threat_model", "verdict"}, "verdict.coverage", "verdict")
}

func TestDiagnoseNativeMemberOneLevelTooDeep(t *testing.T) {
	args := map[string]any{"verdict": map[string]any{
		"verdict":    "CLAIMED",
		"claims":     []any{},
		"set_asides": []any{},
		"coverage":   map[string]any{"revision": "r1", "assessments": []any{}, "threat_model": "Local agent."},
	}}
	data := rejectArgs(t, verdictCallSchema(), args)
	assertMisplaced(t, data, []string{"threat_model"}, "verdict.coverage", "verdict")
	if data["field"] != "verdict.coverage.threat_model" {
		t.Fatalf("field = %v", data["field"])
	}
	if _, ok := data["close_before"]; ok {
		t.Fatal("close_before needs document order, which native arguments lack")
	}
	rep, _ := data["replacement_args"].(map[string]any)
	if rep == nil || tools.ValidateToolArgs(verdictCallSchema(), rep) != nil {
		t.Fatalf("replacement_args = %#v", rep)
	}
}

func TestDiagnoseEncodedTextAtDepth(t *testing.T) {
	args := map[string]any{"verdict": map[string]any{
		"verdict": "CLAIMED", "claims": []any{}, "set_asides": []any{}, "threat_model": "Local agent.",
		"coverage": `{"revision": "r1", "assessments": []}`,
	}}
	data := rejectArgs(t, verdictCallSchema(), args)
	if data["json_encoded"] != true || data["field"] != "verdict.coverage" {
		t.Fatalf("json_encoded = %v field = %v", data["json_encoded"], data["field"])
	}
	if rep, _ := data["replacement_args"].(map[string]any); rep == nil {
		t.Fatal("encoded text was not repaired")
	}
}

func TestDiagnoseAmbiguousDestinationIsNotRelocated(t *testing.T) {
	child := func() map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false,
			"properties": map[string]any{"limit": map[string]any{"type": "integer"}}}
	}
	schema := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
		"read": child(), "write": child(),
	}}
	data := rejectArgs(t, schema, map[string]any{"limit": 3})
	if _, ok := data["misplaced_fields"]; ok {
		t.Fatalf("relocated to one of two equal destinations: %v", data["belongs_under"])
	}
}

func TestDiagnoseOpenObjectsAreNotFlagged(t *testing.T) {
	schema := map[string]any{"type": "object", "additionalProperties": false, "required": []any{"x"},
		"properties": map[string]any{
			"x":    map[string]any{"type": "string"},
			"meta": map[string]any{"type": "object", "additionalProperties": true, "properties": map[string]any{}},
		}}
	data := rejectArgs(t, schema, map[string]any{"meta": map[string]any{"x": "free"}})
	if _, ok := data["misplaced_fields"]; ok {
		t.Fatal("a member of an open object was treated as misplaced")
	}
}

func TestDiagnoseReplacementEchoIsIndentedJSON(t *testing.T) {
	data := rejectArgs(t, verdictCallSchema(), map[string]any{"verdict": unclosedCoverageText(1)})
	raw, _ := data["replacement_args_json"].(string)
	var back map[string]any
	if err := json.Unmarshal([]byte(raw), &back); err != nil || !strings.Contains(raw, "\n  ") {
		t.Fatalf("replacement_args_json = %q (%v)", raw, err)
	}
}

func TestDiagnoseMembersWrittenAfterTheValueClosed(t *testing.T) {
	str := map[string]any{"type": "string"}
	schema := map[string]any{"type": "object", "additionalProperties": false, "required": []any{"brief"}, "properties": map[string]any{
		"agent_type": str,
		"brief": map[string]any{"type": "object", "additionalProperties": false, "required": []any{"goal", "scope"}, "properties": map[string]any{
			"goal":  str,
			"files": map[string]any{"type": "array", "items": str},
			"scope": map[string]any{"type": "object"},
		}},
	}}
	data := rejectArgs(t, schema, map[string]any{"agent_type": "implementer", "brief": `{"goal": "g"}, "files": ["a"], "scope": {"mode": "write"}}`})
	trailing, _ := data["json_trailing_members"].([]string)
	if data["json_malformed"] != true || data["json_trailing_text_at"] != 14 || !slices.Equal(trailing, []string{"files", "scope"}) || data["json_trailing_belongs_under"] != "brief" {
		t.Fatalf("malformed = %v at %v trailing = %v under %v", data["json_malformed"], data["json_trailing_text_at"], trailing, data["json_trailing_belongs_under"])
	}
	rep, _ := data["replacement_args"].(map[string]any)
	brief, _ := rep["brief"].(map[string]any)
	if brief["scope"] == nil || brief["files"] == nil {
		t.Fatalf("replacement brief = %#v, want the trailing members inside it", brief)
	}
}

// A string slot that swallowed the rest of the call: the members after the
// value closed are the call's own arguments.
func TestDiagnoseArgumentsSwallowedByJSONText(t *testing.T) {
	str := map[string]any{"type": "string"}
	schema := map[string]any{"type": "object", "additionalProperties": false, "required": []any{"brief"}, "properties": map[string]any{
		"files": map[string]any{"type": "array", "items": str},
		"scope": map[string]any{"type": "object"},
		"brief": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"goal": str}},
	}}
	data := rejectArgs(t, schema, map[string]any{"brief": `{"goal": "g"}, "files": ["a"], "scope": {"mode": "write"}}`})
	trailing, _ := data["json_trailing_members"].([]string)
	if !slices.Equal(trailing, []string{"files", "scope"}) || data["json_trailing_belongs_under"] != "" {
		t.Fatalf("trailing = %v under %q, want the top level", trailing, data["json_trailing_belongs_under"])
	}
	rep, _ := data["replacement_args"].(map[string]any)
	if rep["files"] == nil || rep["scope"] == nil {
		t.Fatalf("replacement = %#v, want files and scope beside brief", rep)
	}
}
