package guard

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/oarcore"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Every withheld reason reaches the model as exactly one advisory, and none of
// them can stop a tool.
func TestMidTurnNotePoliciesWarnOnce(t *testing.T) {
	pipeline := hostFinishOARPipeline()
	if pipeline == nil {
		t.Fatal("stock OAR pipeline unavailable")
	}
	for _, code := range []string{
		guidance.MidTurnNoteUngroundedCode, guidance.MidTurnNoteHandleUngroundCode, guidance.MidTurnNoteURLOnlyCode,
		guidance.MidTurnNoteArtifactCode, guidance.MidTurnNoteSyntaxCode, guidance.MidTurnNoteEmbedCode, guidance.MidTurnNoteWaitFallbackCode, guidance.MidTurnNoteEvidenceUnreadable,
	} {
		t.Run(code, func(t *testing.T) {
			gc := oar.NewGuardContext()
			gc.Session.SessionID = "coordinator"
			gc.Session.MidTurnCitationsRequired = true
			gc.Rejection.RejectObservation = guidance.MidTurnObservation(code)
			gc.PutRejectData(code, guidance.OffenderHintData([]string{"read#9"}))
			res, err := pipeline.EvaluateBlock(context.Background(), oar.AnchorCoordinatorMidTurnNote, gc)
			testutil.FailErr(t, "evaluate", err)
			if res.Decision == nil || res.Decision.Effect != oar.EffectWarn {
				t.Fatalf("decision = %+v", res.Decision)
			}
			if len(res.Decision.Advisories) != 1 || res.Decision.Advisories[0].Code != code {
				t.Fatalf("advisories = %+v", res.Decision.Advisories)
			}
		})
	}
	t.Run("no observation, no notice", func(t *testing.T) {
		res, err := pipeline.EvaluateBlock(context.Background(), oar.AnchorCoordinatorMidTurnNote, oar.NewGuardContext())
		testutil.FailErr(t, "evaluate", err)
		if res.Decision != nil {
			t.Fatalf("decision = %+v", res.Decision)
		}
	})
}

// A host read failure is not the model's defect, so neither withheld notice may
// send it to fix citations that were never checked.
func TestUnreadableEvidenceCopyNamesNoCitationDefect(t *testing.T) {
	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load stock policy", err)
	facts := map[string]any{"paintedwolf.mid_turn_evidence_unreadable": true, "paintedwolf.mid_turn_citation_attempted": true, "paintedwolf.mid_turn_citations_required": true}
	for _, code := range []string{WaitHiddenTextCode, guidance.MidTurnNoteEvidenceUnreadable} {
		entry, ok := cfg.HintCodes[code]
		if !ok {
			t.Fatalf("%s missing from stock policy", code)
		}
		var body strings.Builder
		for _, field := range []string{entry.What, entry.Cause, entry.Why, entry.Fix, entry.Instead} {
			body.WriteString(oarcore.RenderCopy(field, facts) + "\n")
		}
		for _, blame := range []string{"cited nothing", "did not resolve", "citing what it reports"} {
			if strings.Contains(body.String(), blame) {
				t.Errorf("%s blames citations with %q:\n%s", code, blame, body.String())
			}
		}
		if !strings.Contains(body.String(), "could not read the evidence") {
			t.Errorf("%s omits the read failure:\n%s", code, body.String())
		}
	}
}
