package definition

import "testing"

func TestReviewFollowupRequiresOneBoundedClosureContract(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(*reviewLoopYAML)
		valid bool
	}{
		{name: "bounded questions", valid: true, alter: func(*reviewLoopYAML) {}},
		{name: "conflicting counters", alter: func(raw *reviewLoopYAML) { raw.IterationCap = 2 }},
		{name: "unbounded allowance", alter: func(raw *reviewLoopYAML) { raw.FollowupAttempts = 5 }},
		{name: "missing reviewer", alter: func(raw *reviewLoopYAML) { raw.RequiredAgents = nil }},
		{name: "missing coverage", alter: func(raw *reviewLoopYAML) { delete(raw.VerdictSchema, "coverage") }},
		{name: "missing continuation decision", alter: func(raw *reviewLoopYAML) { raw.VerdictSchema[VerdictDecisionKey] = "CHALLENGED" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := reviewLoopYAML{EvidenceKey: "challenge", FollowupAttempts: 2, ReconcilesPhase: "claims", RequiredAgents: []string{"skeptic"}, VerdictSchema: map[string]string{VerdictDecisionKey: "CHALLENGED|NEEDS_INVESTIGATION", "claims": VerdictClaimsType, "coverage": VerdictCoverageType}, ClaimStatuses: map[string]string{"unresolved": "open"}}
			tc.alter(&raw)
			def, err := parseReviewLoopYAML("challenge", raw)
			if tc.valid {
				if err != nil {
					t.Fatalf("valid follow-up rejected: %v", err)
				}
				if def.FollowupAttempts != 2 || def.IterationCap != 0 {
					t.Fatalf("mixed closure counters: %+v", def)
				}
			} else if err == nil {
				t.Fatal("invalid follow-up accepted")
			}
		})
	}
}
