package feedback

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
)

func matchWhen(t *testing.T, when string, ctx map[string]any) bool {
	t.Helper()
	node, err := parseGateSignalWhen(when)
	testutil.FailErr(t, "parse when "+when, err)
	return evalGateSignalWhen(node, ctx)
}

// The grammar an author actually uses: context keys combined with and/or/not.
func TestGateSignalWhenEvaluatesContextKeys(t *testing.T) {
	ctx := map[string]any{
		"current_gates_known":  true,
		"current_gates_passed": false,
		"current_phase":        "intake",
		"blank_phase":          "   ",
		"failed_leaves":        []string{"a"},
		"no_leaves":            []string{},
	}
	for _, tc := range []struct {
		when string
		want bool
	}{
		{"", true},
		{"true", true},
		{"false", false},
		{"current_gates_known", true},
		{"current_gates_passed", false},
		{"not current_gates_passed", true},
		{"current_phase", true},
		{"blank_phase", false},
		{"failed_leaves", true},
		{"no_leaves", false},
		{"current_gates_known and not current_gates_passed", true},
		{"current_gates_passed or failed_leaves", true},
		{"(current_gates_passed or no_leaves) and current_phase", false},
		// An unknown key is false rather than an error: the same fail-closed
		// reading boolexpr gives every other caller.
		{"never_set_anywhere", false},
	} {
		if got := matchWhen(t, tc.when, ctx); got != tc.want {
			t.Errorf("when %q = %v, want %v", tc.when, got, tc.want)
		}
	}
}

// Template syntax is not part of the grammar, so an expression that tries to
// reach into the surrounding template is a parse failure at load.
func TestGateSignalWhenRefusesTemplateEscapes(t *testing.T) {
	for name, when := range map[string]string{
		"closes the if and opens ssi": `1 %}{% ssi "/etc/hosts" %}{% if 1`,
		"opens an include":            `1 %}{% include "/etc/hosts" %}{% if 1`,
		"raw tag":                     `{% ssi "/etc/hosts" %}`,
		"interpolation":               `{{ 1 }}`,
		"attribute walk":              `context.secrets`,
		"comparison":                  `phase == "intake"`,
		"filter":                      `phase|length`,
		"function call":               `provider_hint_emitted("X")`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseGateSignalWhen(when); err == nil {
				t.Fatalf("parsed %q", when)
			}
		})
	}
}

func TestGateFeedbackDefValidationCompilesWhen(t *testing.T) {
	def := GateFeedbackDef{
		ID:      "human_approval",
		Purpose: "p",
		Satisfy: []string{"s"},
		MissingSignals: []GateMissingSignal{
			{ID: "bad", When: `1 %}{% ssi "/etc/hosts" %}{% if 1`, Detail: "d", Fix: "f"},
		},
	}
	at := extpacks.OnDisk("pack/guidance/gate-feedback")
	err := validateGateFeedbackDef(at, "human_approval.yaml", def)
	if err == nil {
		t.Fatal("validation accepted an unparsable when")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Fatalf("error does not name the row: %v", err)
	}
}
