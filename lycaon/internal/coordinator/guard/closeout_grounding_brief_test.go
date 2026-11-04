package guard_test

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type briefLedgerReader struct{ ledger evidence.Ledger }

func (r briefLedgerReader) LoadLedger(context.Context, string) (evidence.Ledger, error) {
	return r.ledger, nil
}

func (r briefLedgerReader) WorkerLegs(context.Context, string, time.Time) ([]guidance.EvidenceLeg, error) {
	return nil, nil
}

// briefOnlyTurn is a turn whose only source is its opening brief. An earlier turn's
// read remains in the ledger, as it does in a real session.
func briefOnlyTurn(t *testing.T, draft guidance.CloseoutDraft) guard.CloseoutGroundingVerdict {
	t.Helper()
	root := t.TempDir()
	ledger := evidence.AssembleLedger([]evidence.Record{evidence.NewSourceChangeRecord(1), {
		Handle: "read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Fidelity: evidence.FidelityStructured,
		Path: "data/old.txt", LineRanges: []evidence.LineRange{{Start: 1, End: 1}}, Body: []string{"1: old"},
	}})
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "read data/old.txt"},
		{Role: api.MessageRoleTool, Content: "1: old", EvidenceHandles: []string{"read#1"}},
		{Role: api.MessageRoleAssistant, Content: "done"},
		{Role: api.MessageRoleUser, Content: "which files changed?"},
	}
	verdict, err := guard.EvaluateCoordinatorCloseoutGrounding(
		t.Context(), briefLedgerReader{ledger: ledger}, &api.Session{ID: "coordinator", WorkspacePath: root},
		history, "implement_investigate", draft, evidence.CitationRoots{ProjectDir: root},
	)
	testutil.FailErr(t, "evaluate closeout", err)
	return verdict
}

// AC-Q-01: a zero-call answer citing the brief is a traced answer, with the row as
// its evidence, and it needs nothing else from the session's ledger.
func TestCloseoutTracesAnAnswerCitingABriefRowWithNoToolCall(t *testing.T) {
	verdict := briefOnlyTurn(t, markdownDraft("config.txt was changed outside the session [source_change#1]."))
	if verdict.Code != "" || verdict.CitationsRequired {
		t.Fatalf("verdict = %+v, want accepted", verdict)
	}
	grounding := verdict.Grounding
	if grounding == nil || !grounding.Traced || grounding.EvidenceOptional {
		t.Fatalf("grounding = %+v, want traced and not exempt", grounding)
	}
	if len(grounding.CitedEvidence) != 1 || grounding.CitedEvidence[0].Handle != "source_change#1" {
		t.Fatalf("cited evidence = %+v", grounding.CitedEvidence)
	}
	if cited := grounding.CitedEvidence[0]; cited.Path != "" {
		t.Fatalf("a row cites a record, not a file: %+v", cited)
	}
}

// A brief is delivered whether or not anyone asks about it, so its records create
// no citation duty: this turn still has no observation to cite.
func TestBriefRowsDoNotRequireCitationsOnAToolFreeTurn(t *testing.T) {
	verdict := briefOnlyTurn(t, markdownDraft("Thanks, noted."))
	if verdict.Code != "" || verdict.CitationsRequired {
		t.Fatalf("verdict = %+v, want no requirement", verdict)
	}
	if verdict.Grounding == nil || !verdict.Grounding.EvidenceOptional {
		t.Fatalf("grounding = %+v, want evidence optional", verdict.Grounding)
	}
}

// A row establishes that a change was recorded. It supports no line of the file
// and no quotation of the file's content.
func TestBriefRowSupportsNoFileCoordinateOrContent(t *testing.T) {
	for name, markdown := range map[string]string{
		"line":    "See [source_change#1:42].",
		"range":   "See [source_change#1:1-3].",
		"content": `It now reads [source_change#1 "config v2 edited outside"].`,
	} {
		t.Run(name, func(t *testing.T) {
			verdict := briefOnlyTurn(t, markdownDraft(markdown))
			if verdict.Code != guidance.InvestCitationUnverifiableCode || !verdict.CitationUnverifiable {
				t.Fatalf("verdict = %+v, want unverifiable", verdict)
			}
			if verdict.Grounding == nil || verdict.Grounding.Traced {
				t.Fatalf("grounding = %+v, want untraced", verdict.Grounding)
			}
		})
	}
}

// Naming a changed file the session never read stays prose: the brief does not make
// the file itself an observation.
func TestBriefRowDoesNotMakeItsFileCitable(t *testing.T) {
	verdict := briefOnlyTurn(t, markdownDraft("[config.txt] changed."))
	if verdict.Code != "" || verdict.Grounding == nil || !verdict.Grounding.EvidenceOptional {
		t.Fatalf("verdict = %+v, want the bracket left as prose", verdict)
	}
	if got := len(verdict.Resolved.Occurrences); got != 0 {
		t.Fatalf("occurrences = %d, want none", got)
	}
}
