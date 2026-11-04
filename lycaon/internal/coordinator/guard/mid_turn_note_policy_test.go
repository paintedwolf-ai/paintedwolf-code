package guard

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
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
		guidance.MidTurnNoteArtifactCode, guidance.MidTurnNoteSyntaxCode, guidance.MidTurnNoteEmbedCode, guidance.MidTurnNoteWaitFallbackCode,
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
