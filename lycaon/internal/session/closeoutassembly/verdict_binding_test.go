package closeoutassembly

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBindReviewerVerdictEvidenceUsesEachReviewersLedger(t *testing.T) {
	reviewers := []guidance.ReviewerEvidence{
		{
			Agent:  "skeptic",
			LegIDs: []string{"leg-skeptic"},
			Ledgers: []evidence.Ledger{evidence.AssembleLedger([]evidence.Record{{
				Handle: "read#1", Kind: "read", Path: "auth/session.go",
			}})},
		},
		{
			Agent:  "web-researcher",
			LegIDs: []string{"leg-web"},
			Ledgers: []evidence.Ledger{evidence.AssembleLedger([]evidence.Record{{
				Handle: "web_search#1", Kind: "web_search", URL: "https://example.com/advisory",
			}})},
		},
	}
	union := evidence.AssembleLedger([]evidence.Record{
		{Handle: "leg-skeptic:read#1", Kind: "read", Path: "auth/session.go"},
		{Handle: "leg-web:web_search#1", Kind: "web_search", URL: "https://example.com/advisory"},
	})

	cited, urls, assembled := bindObservedReviewerVerdictEvidence(nil, nil, reviewers, union)
	if !assembled {
		t.Fatal("expected host assembly")
	}
	if len(cited) != 2 || cited[0].Handle != "leg-skeptic:read#1" || cited[1].Handle != "leg-web:web_search#1" {
		t.Fatalf("cited evidence = %+v", cited)
	}
	if len(urls) != 0 {
		t.Fatalf("URLs = %v want handles preferred", urls)
	}
}

func TestBindReviewerVerdictEvidencePreservesExplicitCitation(t *testing.T) {
	explicit := []api.CitationGroundingCitedEvidence{{Handle: "invented:read#99"}}
	reviewers := []guidance.ReviewerEvidence{{
		Agent:  "skeptic",
		LegIDs: []string{"leg-skeptic"},
		Ledgers: []evidence.Ledger{evidence.AssembleLedger([]evidence.Record{{
			Handle: "read#1", Kind: "read", Path: "auth/session.go",
		}})},
	}}

	cited, _, assembled := bindObservedReviewerVerdictEvidence(explicit, nil, reviewers, evidence.InitLedger())
	if !assembled || len(cited) != 2 {
		t.Fatalf("assembled/cited = %v/%+v", assembled, cited)
	}
	if cited[0].Handle != explicit[0].Handle {
		t.Fatalf("explicit citation was rewritten: %+v", cited)
	}
}

func TestBindReviewerVerdictEvidenceFallsBackToCoordinatorLedger(t *testing.T) {
	union := evidence.AssembleLedger([]evidence.Record{{Handle: "read#1", Kind: "read", Path: "main.go"}})
	cited, _, assembled := bindObservedReviewerVerdictEvidence(nil, nil, nil, union)
	if !assembled || len(cited) != 1 || cited[0].Handle != "read#1" {
		t.Fatalf("assembled/cited = %v/%+v", assembled, cited)
	}
}
