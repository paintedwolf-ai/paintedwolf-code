package guidance_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCloseoutCitationsSubsetOfPrior_newLineAllows(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "audit", Visibility: api.MessageVisibilityTranscript},
		{
			Role:       api.MessageRoleAssistant,
			Content:    "first report",
			Visibility: api.MessageVisibilityTranscript,
			Grounding: &api.CitationGrounding{
				CitedEvidence: []api.CitationGroundingCitedEvidence{{Path: "internal/foo.go", Line: 1}},
			},
		},
	}
	report := guidance.CoordinatorCompletionReport{
		Synthesis: "found bar too",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{
			{Path: "internal/foo.go", Line: 1},
			{Path: "internal/foo.go", Line: 42},
		},
	}
	hit, _ := guidance.CloseoutCitationsSubsetOfPrior(history, report)
	if hit {
		t.Fatal("new line in an already-cited file must not count as no-new-evidence")
	}
}

func TestCloseoutCitationsSubsetOfPrior_equalBlocks(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "audit", Visibility: api.MessageVisibilityTranscript},
		{
			Role:       api.MessageRoleAssistant,
			Content:    "first report",
			Visibility: api.MessageVisibilityTranscript,
			Grounding: &api.CitationGrounding{
				CitedEvidence: []api.CitationGroundingCitedEvidence{{Path: "AGENTS.md"}, {Path: "docs/a.md"}},
			},
		},
	}
	report := guidance.CoordinatorCompletionReport{
		Synthesis: "same again",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{
			{Path: "AGENTS.md"},
			{Path: "docs/a.md"},
		},
	}
	hit, offenders := guidance.CloseoutCitationsSubsetOfPrior(history, report)
	if !hit {
		t.Fatal("equal citation set must count as no-new-evidence")
	}
	if len(offenders) != 2 {
		t.Fatalf("offenders = %v want 2", offenders)
	}
}

func TestCloseoutCitationsSubsetOfPrior_newPathAllows(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "audit", Visibility: api.MessageVisibilityTranscript},
		{
			Role:       api.MessageRoleAssistant,
			Content:    "first report",
			Visibility: api.MessageVisibilityTranscript,
			Grounding: &api.CitationGrounding{
				CitedEvidence: []api.CitationGroundingCitedEvidence{{Path: "AGENTS.md"}},
			},
		},
	}
	report := guidance.CoordinatorCompletionReport{
		Synthesis: "delta",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{
			{Path: "AGENTS.md"},
			{Path: "lycaon/AGENTS.md"},
		},
	}
	hit, _ := guidance.CloseoutCitationsSubsetOfPrior(history, report)
	if hit {
		t.Fatal("new path must not count as subset")
	}
}

func TestCloseoutCitationsSubsetOfPrior_noPriorAllows(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "audit", Visibility: api.MessageVisibilityTranscript},
	}
	report := guidance.CoordinatorCompletionReport{
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{Path: "AGENTS.md"}},
	}
	hit, _ := guidance.CloseoutCitationsSubsetOfPrior(history, report)
	if hit {
		t.Fatal("first closeout must not fire no-new-evidence")
	}
}

func TestCloseoutCitationsSubsetOfPrior_ignoresAgentNote(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "audit", Visibility: api.MessageVisibilityTranscript},
		{
			Role:       api.MessageRoleAssistant,
			Kind:       api.MessageKindAgentNote,
			Content:    "A verified milestone",
			Visibility: api.MessageVisibilityTranscript,
			Grounding: &api.CitationGrounding{
				CitedEvidence: []api.CitationGroundingCitedEvidence{{Path: "AGENTS.md"}},
			},
		},
	}
	report := guidance.CoordinatorCompletionReport{
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{Path: "AGENTS.md"}},
	}
	hit, _ := guidance.CloseoutCitationsSubsetOfPrior(history, report)
	if hit {
		t.Fatal("an agent note is not a prior closeout")
	}
}

func TestCloseoutNoNewEvidenceCode(t *testing.T) {
	if got := guidance.CloseoutNoNewEvidenceCode("implement_investigate"); got != guidance.InvestNoNewEvidenceCode {
		t.Fatalf("investigate code = %q", got)
	}
	if got := guidance.CloseoutNoNewEvidenceCode("implement_synthesis"); got != guidance.SynthNoNewEvidenceCode {
		t.Fatalf("synthesis code = %q", got)
	}
}
