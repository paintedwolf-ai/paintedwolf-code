package contract

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/oar"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// shippedRuleDocument returns the stock policy document for code as JSON.
func shippedRuleDocument(t *testing.T, catalog *extpacks.EffectiveCatalog, code string) json.RawMessage {
	t.Helper()
	content, _, ok := catalog.UnitContent(extpacks.PolicyUnitID(code))
	if !ok {
		t.Fatalf("%s is not a stock policy unit", code)
	}
	var doc map[string]any
	contractcheck.FailErr(t, "decode "+code, yaml.Unmarshal(content, &doc))
	raw, err := json.Marshal(doc)
	contractcheck.FailErr(t, "encode "+code, err)
	return raw
}

// TestOARShippedRulesDecideHandWrittenOccurrences runs the shipped rule documents through
// the host conformance runner against hand-written occurrences and decisions.
func TestOARShippedRulesDecideHandWrittenOccurrences(t *testing.T) {
	t.Parallel()
	catalog := contractcheck.StockCatalog(t)
	cr, err := oar.NewHostConformanceRunner(filepath.Join(contractcheck.RepoRoot(t), "schemas"), oar.InstalledEngineDeclaration())
	contractcheck.FailErr(t, "conformance runner", err)

	type expectation struct {
		rejection string
		decision  string
		code      string
	}
	cases := []struct {
		rule   string
		expect []expectation
	}{
		{"SPEC_POSTURE_STATE_FORBIDDEN", []expectation{
			{"SPEC_POSTURE_STATE_FORBIDDEN", "block", "SPEC_POSTURE_STATE_FORBIDDEN"},
			{"SPEC_POSTURE_UNRESOLVED", "none", ""},
		}},
		{"SPEC_POSTURE_DELEGATION_FORBIDDEN", []expectation{
			{"SPEC_POSTURE_DELEGATION_FORBIDDEN", "block", "SPEC_POSTURE_DELEGATION_FORBIDDEN"},
			{"SPEC_POSTURE_HANDOFF_FORBIDDEN", "none", ""},
		}},
		{"SPEC_POSTURE_HANDOFF_FORBIDDEN", []expectation{
			{"SPEC_POSTURE_HANDOFF_FORBIDDEN", "block", "SPEC_POSTURE_HANDOFF_FORBIDDEN"},
			{"SPEC_POSTURE_DELEGATION_FORBIDDEN", "none", ""},
		}},
		{"SPEC_POSTURE_IMPLEMENT_FORBIDDEN", []expectation{
			{"SPEC_POSTURE_IMPLEMENT_FORBIDDEN", "block", "SPEC_POSTURE_IMPLEMENT_FORBIDDEN"},
			{"SPEC_POSTURE_STUB_REQUIRED", "none", ""},
		}},
		{"SPEC_POSTURE_UNRESOLVED", []expectation{
			{"SPEC_POSTURE_UNRESOLVED", "block", "SPEC_POSTURE_UNRESOLVED"},
			{"SPEC_POSTURE_STATE_FORBIDDEN", "none", ""},
		}},
		{"SPEC_POSTURE_STUB_REQUIRED", []expectation{
			{"SPEC_POSTURE_STUB_REQUIRED", "block", "SPEC_POSTURE_STUB_REQUIRED"},
			{"SPEC_POSTURE_IMPLEMENT_FORBIDDEN", "none", ""},
		}},
		{"DISALLOWED_AGENT", []expectation{
			{"DISALLOWED_AGENT", "block", "DISALLOWED_AGENT"},
			{"SPEC_POSTURE_UNRESOLVED", "none", ""},
		}},
	}
	for _, tc := range cases {
		rule := shippedRuleDocument(t, catalog, tc.rule)
		for _, want := range tc.expect {
			expected := map[string]json.RawMessage{"decision": json.RawMessage(`"` + want.decision + `"`)}
			if want.code != "" {
				expected["code"] = json.RawMessage(`"` + want.code + `"`)
			}
			fx := oar.ConformanceFixture{
				ID:   tc.rule + "/" + want.rejection,
				Rule: rule,
				Input: oar.FixtureInput{
					Anchor: oar.AnchorToolRejected,
					Facts:  map[string]any{"paintedwolf.rejection_code": want.rejection},
				},
				Expected: expected,
			}
			if err := cr.RunFixture(t.Context(), fx); err != nil {
				t.Errorf("%s: %v", fx.ID, err)
			}
		}
	}
}
