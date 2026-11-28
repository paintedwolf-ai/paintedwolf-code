package invariants_test

import (
	"testing"

	"github.com/lycaon/lycaon/test/contract/invariants"
	"github.com/lycaon/lycaon/test/contract/invariants/forbiddencalls"
	"github.com/lycaon/lycaon/test/contract/invariants/invariantstest"
)

var groundingEscalationTree = map[string]string{
	"lycaon/internal/grounding/state.go": "package grounding\n\ntype StateStore struct{}\n\nfunc (*StateStore) IsEscalated(string) bool { return false }\n",
	"lycaon/internal/coordinator/grounding/ambient.go": "package coordgrounding\n\n" +
		"type cb struct{ EscalateMode string }\ntype cfg struct{ CircuitBreaker cb }\ntype state struct{}\n" +
		"func (*state) IsEscalated(string) bool { return false }\n" +
		"type AmbientGroundingCoordinator struct{ Config cfg; State *state }\n" +
		"func (g *AmbientGroundingCoordinator) IsEscalated(id string) bool {\n" +
		"\tif g.Config.CircuitBreaker.EscalateMode != \"block\" { return false }\n" +
		"\treturn g.State.IsEscalated(id)\n}\n",
	"lycaon/cmd/stub/main.go": "package main\n\nfunc main() {}\n",
	"lycaon/pkg/stub/stub.go": "package stub\n",
}

// TestGroundingEscalationInvariantFailClosed is the standard fixture self-test
// for GB10 I5 static row: clean, seeded violations, renamed anchor, stale
// exemption, empty selection, and broken source.
func TestGroundingEscalationInvariantFailClosed(t *testing.T) {
	t.Parallel()
	rule := invariantstest.Rule(t, forbiddencalls.Family, "blocking-escalation-reads-are-mode-aware")

	withExtraExemption := rule
	withExtraExemption.Exempt = append([]invariants.Exemption(nil), rule.Exempt...)
	withExtraExemption.Exempt = append(withExtraExemption.Exempt, invariants.Exemption{
		Key:    "internal/stale/unused.go",
		Reason: "fixture stale exemption check",
	})

	for name, tc := range map[string]struct {
		rule  invariants.Invariant
		files map[string]string
		want  invariantstest.Outcome
	}{
		"clean": {
			rule:  rule,
			files: groundingEscalationTree,
			want:  invariantstest.Passes,
		},
		"seeded violation unregistered caller": {
			rule: rule,
			files: invariantstest.With(groundingEscalationTree, map[string]string{
				"lycaon/internal/unregistered/gate.go": "package unregistered\n\n" +
					"type checker interface{ IsEscalated(string) bool }\n" +
					"func block(c checker) bool { return c.IsEscalated(\"s1\") }\n",
			}),
			want: invariantstest.Violates,
		},
		"seeded violation mode-unaware IsEscalated": {
			rule: rule,
			files: invariantstest.With(groundingEscalationTree, map[string]string{
				"lycaon/internal/coordinator/grounding/ambient.go": "package coordgrounding\n\n" +
					"type state struct{}\nfunc (*state) IsEscalated(string) bool { return false }\n" +
					"type AmbientGroundingCoordinator struct{ State *state }\n" +
					"func (g *AmbientGroundingCoordinator) IsEscalated(id string) bool { return g.State.IsEscalated(id) }\n",
			}),
			want: invariantstest.Violates,
		},
		"renamed anchor": {
			rule: rule,
			files: invariantstest.With(groundingEscalationTree, map[string]string{
				"lycaon/internal/grounding/state.go": "package grounding\n\ntype StateStore struct{}\n\nfunc (*StateStore) IsBlocked(string) bool { return false }\n",
			}),
			want: invariantstest.MissesAnchor,
		},
		"stale exemption": {
			rule:  withExtraExemption,
			files: groundingEscalationTree,
			want:  invariantstest.StaleExemption,
		},
		"empty selection": {
			rule:  rule,
			files: nil,
			want:  invariantstest.Fails,
		},
		"broken source": {
			rule: rule,
			files: invariantstest.With(groundingEscalationTree, map[string]string{
				"lycaon/internal/unregistered/broken.go": "package unregistered\n\nfunc (\n",
			}),
			want: invariantstest.Fails,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			invariantstest.Expect(t, invariants.Evaluate(invariantstest.Fixture(t, tc.files), tc.rule), tc.want, 1)
		})
	}
}
