package commandinvoke

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestValidateArgumentsStrictlyNormalizesDeclaredValues(t *testing.T) {
	minimum, maximum := 1.0, 3.0
	fields := []contribution.InputField{
		{ID: "title", Type: contribution.PropertyString, Required: true},
		{ID: "priority", Type: contribution.PropertyEnum, Values: []string{"low", "high"}, Required: true},
		{ID: "count", Type: contribution.PropertyNumber, Min: &minimum, Max: &maximum},
		{ID: "labels", Type: contribution.PropertyStringList},
		{ID: "target", Type: contribution.PropertyProjectPath, Required: true},
	}
	resolved, err := ValidateArguments(fields, map[string]any{
		"title": "Ship", "priority": "high", "count": 2,
		"labels": []any{"release", "urgent"}, "target": map[string]any{"root_id": "root", "path": "docs/plan.md"},
	}, func(rootID, path string) (string, string, error) { return "root-1", "docs/plan.md", nil })
	testutil.FailErr(t, "ValidateArguments", err)
	if resolved["target"].(map[string]any)["root_id"] != "root-1" {
		t.Fatalf("resolved = %+v", resolved)
	}

	bad := []map[string]any{
		{"unknown": true},
		{"title": "Ship", "priority": "medium", "target": "a"},
		{"title": "Ship", "priority": "high", "count": 4, "target": "a"},
	}
	for _, args := range bad {
		if _, err := ValidateArguments(fields, args, func(_, path string) (string, string, error) { return "root", path, nil }); err == nil {
			t.Errorf("invalid arguments accepted: %+v", args)
		}
	}
}

func TestValidateArgumentsEnforcesBounds(t *testing.T) {
	fields := []contribution.InputField{{ID: "value", Type: contribution.PropertyString, Required: true}}
	if _, err := ValidateArguments(fields, map[string]any{"value": strings.Repeat("x", maxStringRunes+1)}, nil); err == nil {
		t.Fatal("oversized string accepted")
	}
	if _, err := ValidateArguments(nil, map[string]any{"x": strings.Repeat("x", maxArgumentBytes)}, nil); err == nil {
		t.Fatal("oversized envelope accepted")
	}
}

func TestValidateTypedOperationOutput(t *testing.T) {
	fields := []contribution.OutputField{{ID: "issue-id", Type: contribution.PropertyString, Required: true}}
	testutil.FailErr(t, "validate output", ValidateOutput(fields, `{"issue-id":"I-12"}`))
	for _, raw := range []string{`not json`, `{}`, `{"issue-id":12}`, `{"issue-id":"I-12","extra":true}`, `{"issue-id":"I-12"} {}`} {
		if err := ValidateOutput(fields, raw); err == nil {
			t.Errorf("invalid output accepted: %s", raw)
		}
	}
}

func TestPrepareOutputBoundsUntypedTextAndRejectsOversizedTypedJSON(t *testing.T) {
	long := strings.Repeat("界", maxArgumentBytes)
	prepared, err := PrepareOutput(nil, long)
	testutil.FailErr(t, "prepare untyped output", err)
	if len(prepared) > maxArgumentBytes || !strings.HasSuffix(prepared, runeclamp.TruncatedSuffix) || !utf8.ValidString(prepared) {
		t.Fatalf("prepared output len=%d suffix=%t valid=%t", len(prepared), strings.HasSuffix(prepared, runeclamp.TruncatedSuffix), utf8.ValidString(prepared))
	}

	typed := []contribution.OutputField{{ID: "body", Type: contribution.PropertyString, Required: true}}
	if _, err := PrepareOutput(typed, `{"body":"`+long+`"}`); err == nil {
		t.Fatal("oversized typed output must fail instead of truncating invalid JSON")
	}
}

func TestValidateRequiredConfirmationsIgnoresInactiveSteps(t *testing.T) {
	interaction := &contribution.Interaction{Steps: []contribution.InteractionStep{
		{ID: "publish", Kind: contribution.InteractionBoolean},
		{ID: "confirm", Kind: contribution.InteractionConfirmation, Required: true, If: &contribution.InteractionPredicate{Step: "publish", Is: true}},
	}}
	inactive := contribution.InteractionFieldsForAnswers(interaction, map[string]any{"publish": false})
	testutil.FailErr(t, "validate inactive confirmation", ValidateRequiredConfirmations(interaction, inactive, map[string]any{"publish": false}))

	active := contribution.InteractionFieldsForAnswers(interaction, map[string]any{"publish": true})
	if err := ValidateRequiredConfirmations(interaction, active, map[string]any{"publish": true}); err == nil {
		t.Fatal("active required confirmation must be accepted")
	}
	testutil.FailErr(t, "validate accepted confirmation", ValidateRequiredConfirmations(interaction, active, map[string]any{"publish": true, "confirm": true}))
}
