package argdiag_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/argdiag"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func requiredSummary(t *testing.T, schema, args map[string]any) string {
	t.Helper()
	err := tools.ValidateToolArgs(schema, args)
	var invalid *jsonschema.ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected validation failure: %v", err)
	}
	return argdiag.MissingRequiredSummary(invalid, schema, args)
}

func TestRequiredArgumentsObserveRootFailure(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []any{"content"}, "properties": map[string]any{"content": map[string]any{"type": "string"}}}
	args := map[string]any{"label": "existing"}
	before, _ := json.Marshal(args)
	got := requiredSummary(t, schema, args)
	data := rejectArgs(t, schema, args)
	if data["missing_required_arguments"] != got {
		t.Fatalf("validator lost required observations: %#v", data)
	}
	var facts struct {
		Missing  []string          `json:"missing_required"`
		Types    map[string]string `json:"expected_types"`
		Received []string          `json:"received_keys"`
	}
	testutil.FailErr(t, "decode facts", json.Unmarshal([]byte(got), &facts))
	if len(facts.Missing) != 1 || facts.Missing[0] != "content" || facts.Types["content"] != "string" || len(facts.Received) != 1 || facts.Received[0] != "label" {
		t.Fatalf("wrong schema facts: %s", got)
	}
	after, _ := json.Marshal(args)
	if string(before) != string(after) {
		t.Fatal("diagnostics mutated arguments")
	}
	if got := argdiag.MissingRequiredSummary(nil, schema, args); got != "" {
		t.Fatalf("invented failure: %q", got)
	}
}

func TestRequiredArgumentsDoNotFlattenAlternativesOrNestedFailures(t *testing.T) {
	for _, tc := range []struct{ schema, args map[string]any }{
		{map[string]any{"type": "object", "anyOf": []any{map[string]any{"required": []any{"left"}}, map[string]any{"required": []any{"right"}}}}, map[string]any{}},
		{map[string]any{"type": "object", "required": []any{"payload"}, "properties": map[string]any{"payload": map[string]any{"type": "object", "required": []any{"content"}}}}, map[string]any{"payload": map[string]any{}}},
		{map[string]any{"type": "object", "required": []any{"content"}, "properties": map[string]any{"content": map[string]any{"type": "string"}}}, map[string]any{"content": 1}},
	} {
		if got := requiredSummary(t, tc.schema, tc.args); got != "" {
			t.Fatalf("invented unconditional requirements: %s", got)
		}
	}
}

func TestRequiredArgumentsBoundEncodedFacts(t *testing.T) {
	required := []any{"content"}
	for i := 0; i < 40; i++ {
		required = append(required, fmt.Sprintf("z%d-%s", i, strings.Repeat("\x00", 90)))
	}
	schema := map[string]any{"type": "object", "required": required, "properties": map[string]any{"content": map[string]any{"type": "string"}}}
	args := map[string]any{}
	for i := 0; i < 20; i++ {
		args[fmt.Sprintf("key%d-%s", i, strings.Repeat("\x00", 90))] = true
	}
	got := requiredSummary(t, schema, args)
	if len(got) > 1024 || !json.Valid([]byte(got)) {
		t.Fatalf("invalid bounded summary: %d bytes", len(got))
	}
	var facts map[string]any
	testutil.FailErr(t, "decode bounded summary", json.Unmarshal([]byte(got), &facts))
	if len(facts["missing_required"].([]any))+int(facts["missing_required_omitted"].(float64)) != len(required) {
		t.Fatalf("missing count lost: %s", got)
	}
	if len(facts["received_keys"].([]any))+int(facts["received_keys_omitted"].(float64)) != len(args) {
		t.Fatalf("received count lost: %s", got)
	}
}

func TestRequiredArgumentsDoNotGuessTypes(t *testing.T) {
	schema := map[string]any{
		"type": "object", "required": []any{"ref", "union", "undeclared"},
		"$defs": map[string]any{"text": map[string]any{"type": "string"}},
		"properties": map[string]any{
			"ref":   map[string]any{"$ref": "#/$defs/text", "type": "string"},
			"union": map[string]any{"type": []any{"string", "null"}},
		},
	}
	got := requiredSummary(t, schema, map[string]any{})
	if strings.Contains(got, "expected_types") || !strings.Contains(got, `"ref","undeclared","union"`) {
		t.Fatalf("types guessed or missing keys lost: %s", got)
	}
}
