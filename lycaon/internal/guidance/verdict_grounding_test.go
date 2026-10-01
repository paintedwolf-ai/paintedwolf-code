package guidance_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

func verdictUnion() guidance.CloseoutEvidence {
	ev := guidance.CloseoutEvidence{Ledger: evidence.InitLedger()}
	ev.Handles["leg-1:read#1"] = evidence.Record{Handle: "leg-1:read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Body: []string{"token check"}}
	ev.Handles["leg-2:fetch#1"] = evidence.Record{Handle: "leg-2:fetch#1", URL: "https://example.com/advisory"}
	return ev
}

func skepticLedger() evidence.Ledger {
	l := evidence.InitLedger()
	l.Handles["read#1"] = evidence.Record{Handle: "read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Body: []string{"token check"}}
	return l
}

func researcherLedger() evidence.Ledger {
	l := evidence.InitLedger()
	l.Handles["fetch#1"] = evidence.Record{Handle: "fetch#1", URL: "https://example.com/advisory"}
	return l
}

func TestEvaluateVerdictGroundingRequiresCitations(t *testing.T) {
	t.Parallel()
	out := guidance.EvaluateVerdictGrounding(evidence.CitationRoots{}, verdictUnion(), nil, nil,
		[]guidance.ReviewerEvidence{{Agent: "skeptic", LegIDs: []string{"leg-1"}}})
	if out.Code != guidance.VerdictCitationsRequiredCode {
		t.Fatalf("code = %q want %q", out.Code, guidance.VerdictCitationsRequiredCode)
	}
	if len(out.UncitedReviewers) != 1 || out.UncitedReviewers[0] != "skeptic" {
		t.Fatalf("uncited = %v", out.UncitedReviewers)
	}
}

func TestEvaluateVerdictGroundingRejectsInventedReferences(t *testing.T) {
	t.Parallel()
	out := guidance.EvaluateVerdictGrounding(evidence.CitationRoots{}, verdictUnion(),
		[]api.CitationGroundingCitedEvidence{{Handle: "nope#9"}}, nil, nil)
	if out.Code != guidance.VerdictCitationUngroundedCode {
		t.Fatalf("code = %q want %q", out.Code, guidance.VerdictCitationUngroundedCode)
	}
	if out.UngroundedCount != 1 || len(out.UngroundedSample) != 1 {
		t.Fatalf("ungrounded = %d sample=%v", out.UngroundedCount, out.UngroundedSample)
	}

	out = guidance.EvaluateVerdictGrounding(evidence.CitationRoots{}, verdictUnion(),
		nil, []string{"https://example.com/never-fetched"}, nil)
	if out.Code != guidance.VerdictCitationUngroundedCode {
		t.Fatalf("url code = %q want %q", out.Code, guidance.VerdictCitationUngroundedCode)
	}
}

func TestEvaluateVerdictGroundingReviewerCoverage(t *testing.T) {
	t.Parallel()
	reviewers := []guidance.ReviewerEvidence{
		{Agent: "skeptic", LegIDs: []string{"leg-1"}, Ledgers: []evidence.Ledger{skepticLedger()}},
		{Agent: "web-researcher", LegIDs: []string{"leg-2"}, Ledgers: []evidence.Ledger{researcherLedger()}},
	}

	// Skeptic covered by a leg-qualified handle; researcher uncited.
	out := guidance.EvaluateVerdictGrounding(evidence.CitationRoots{}, verdictUnion(),
		[]api.CitationGroundingCitedEvidence{{Handle: "leg-1:read#1", Excerpt: "token check"}}, nil, reviewers)
	if out.Code != guidance.VerdictReviewerUncitedCode {
		t.Fatalf("code = %q want %q", out.Code, guidance.VerdictReviewerUncitedCode)
	}
	if len(out.UncitedReviewers) != 1 || out.UncitedReviewers[0] != "web-researcher" {
		t.Fatalf("uncited = %v", out.UncitedReviewers)
	}

	// A cited URL the researcher's leg fetched completes coverage.
	out = guidance.EvaluateVerdictGrounding(evidence.CitationRoots{}, verdictUnion(),
		[]api.CitationGroundingCitedEvidence{{Handle: "leg-1:read#1", Excerpt: "token check"}},
		[]string{"https://example.com/advisory"}, reviewers)
	if out.Code != "" {
		t.Fatalf("code = %q want grounded (sample=%v uncited=%v)", out.Code, out.UngroundedSample, out.UncitedReviewers)
	}
	g := out.Grounding
	if g == nil || !g.Traced {
		t.Fatalf("grounding = %+v want traced", g)
	}
	if len(g.CitedEvidence) != 1 || g.CitedEvidence[0].Handle != "leg-1:read#1" {
		t.Fatalf("cited evidence = %+v", g.CitedEvidence)
	}
	if len(g.CitedURLs) != 1 || g.CitedURLs[0] != "https://example.com/advisory" {
		t.Fatalf("cited urls = %+v", g.CitedURLs)
	}
	if len(g.Checks) != 2 {
		t.Fatalf("checks = %+v", g.Checks)
	}
}

func TestEvaluateVerdictGroundingVacuousReviewerCoverage(t *testing.T) {
	t.Parallel()
	// No owed reviewers: citations still required and validated, coverage vacuous.
	out := guidance.EvaluateVerdictGrounding(evidence.CitationRoots{}, verdictUnion(),
		[]api.CitationGroundingCitedEvidence{{Handle: "leg-1:read#1", Excerpt: "token check"}}, nil, nil)
	if out.Code != "" || out.Grounding == nil {
		t.Fatalf("code = %q grounding = %v", out.Code, out.Grounding)
	}
}
