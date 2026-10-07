package definition

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

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

func TestCoverageReviewersRequireDeclaredCoverageAndReviewer(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(*reviewLoopYAML)
		valid bool
	}{
		{"declared", func(*reviewLoopYAML) {}, true},
		{"not required", func(r *reviewLoopYAML) { r.RequiredAgents = nil }, false},
		{"no candidate", func(r *reviewLoopYAML) { r.ReconcilesPhase = "" }, false},
		{"no coverage", func(r *reviewLoopYAML) { delete(r.VerdictSchema, "coverage") }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := reviewLoopYAML{EvidenceKey: "check", ReconcilesPhase: "candidate", RequiredAgents: []string{"auditor"}, CoverageReviewers: []string{"auditor"}, VerdictSchema: map[string]string{"verdict": "DONE", "coverage": VerdictCoverageType}}
			tc.alter(&raw)
			def, err := parseReviewLoopYAML("check", raw)
			if (err == nil) != tc.valid {
				t.Fatalf("parse = %+v, %v", def, err)
			}
			if tc.valid {
				copied := cloneReviewLoop(def)
				copied.CoverageReviewers[0] = "changed"
				if def.CoverageReviewers[0] != "auditor" {
					t.Fatal("reviewer roster aliases cloned definition")
				}
			}
		})
	}
}

func TestCoverageContractSurvivesManifestStorageAndPhaseMerge(t *testing.T) {
	raw := []byte(`id: generic-review
version: 1.0.0
phases:
  - id: candidate
    activity_label: Draft assessment
    next: check
    review_loop:
      evidence_key: candidate
      verdict_schema: {verdict: DONE, coverage: coverage_review}
  - id: check
    activity_label: Review assessment
    review_loop:
      evidence_key: check
      reconciles_phase: candidate
      required_agents: [auditor]
      coverage_reviewers: [auditor]
      verdict_schema: {verdict: DONE, coverage: coverage_review}
`)
	manifest, err := ParseManifestYAML(raw)
	testutil.FailErr(t, "parse generic coverage workflow", err)
	encoded, err := MarshalManifestYAML(manifest)
	testutil.FailErr(t, "persist generic coverage workflow", err)
	restored, err := ParseManifestYAML([]byte(encoded))
	testutil.FailErr(t, "restore generic coverage workflow", err)
	original := manifest.PhaseDefs[1]
	merged := MergePhaseDef(PhaseDef{}, original)
	if !reflect.DeepEqual(original.ReviewLoop, restored.PhaseDefs[1].ReviewLoop) || !reflect.DeepEqual(original.ReviewLoop, merged.ReviewLoop) {
		t.Fatal("coverage contract lost during storage or phase merge")
	}
}
