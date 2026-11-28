package promptloop

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/session/closeouts"
)

type gb07PromptloopWording struct {
	name   string
	prefix string
}

var gb07PromptloopD5Wordings = []gb07PromptloopWording{
	{name: "confident", prefix: "Verified:"},
	{name: "hedged", prefix: "Appears verified:"},
	{name: "completion_asserting", prefix: "Complete and verified:"},
}

// TestGB07InProcessPresentationRepairZeroFriction covers G04-10 across all 3 D5 wordings (3 subtests):
// Presentation repair increments closeout retry_count without charging shared friction_units,
// whereas citation refusal charges shared friction_units.
func TestGB07InProcessPresentationRepairZeroFriction(t *testing.T) {
	t.Run("G04-10-inprocess-presentation-repair-zero-friction", func(t *testing.T) {
		type sig struct {
			embedRetryAttempt      int
			embedFrictionCalls     int
			embedKick              anchor.ID
			imageRetryAttempt      int
			imageFrictionCalls     int
			citationRetryAttempt   int
			citationFrictionCalls  int
		}
		var sigs []sig
		for _, w := range gb07PromptloopD5Wordings {
			t.Run(w.name, func(t *testing.T) {
				budgetSvc := closeouts.New(nil)
				frictionCalls := 0
				tc := &turnCloseout{
					Deps: CloseoutDeps{
						CloseoutTrackingDeps: CloseoutTrackingDeps{
							RecordGroundingFriction: func(ctx context.Context, sid string) guidance.GroundingFriction {
								frictionCalls++
								return budgetSvc.RecordGroundingFriction(ctx, sid)
							},
						},
					},
				}
				sid := "gb07-g04-10-" + w.name
				st := &promptLoopTurnState{}
				draft := guidance.CloseoutDraft{}

				// 1. Presentation refusal (markdown embed) -> retry_count increments, 0 friction
				bEmbed := tc.closeoutRetryBudget(t.Context(), sid, st, guidance.PresentMarkdownEmbedCode, draft)
				if bEmbed.kick != anchor.CoordinatorPresentationRepair || bEmbed.friction != nil || frictionCalls != 0 || st.closeoutRetry.attempt != 1 {
					t.Fatalf("embed presentation refusal: budget=%+v attempt=%d frictionCalls=%d",
						bEmbed, st.closeoutRetry.attempt, frictionCalls)
				}
				embedAttempt := st.closeoutRetry.attempt
				embedFriction := frictionCalls

				// 2. Presentation refusal (unobserved image) -> retry_count increments, still 0 friction
				bImage := tc.closeoutRetryBudget(t.Context(), sid, st, guidance.PresentImageUnobservedCode, draft)
				if bImage.kick != anchor.CoordinatorPresentationRepair || bImage.friction != nil || frictionCalls != 0 || st.closeoutRetry.attempt != 2 {
					t.Fatalf("image presentation refusal: budget=%+v attempt=%d frictionCalls=%d",
						bImage, st.closeoutRetry.attempt, frictionCalls)
				}
				imageAttempt := st.closeoutRetry.attempt
				imageFriction := frictionCalls

				// 3. Contrast: citation refusal DOES charge shared friction_units
				bCite := tc.closeoutRetryBudget(t.Context(), sid, st, guidance.SynthHandleNotInLegsCode, draft)
				if bCite.kick != anchor.CoordinatorCitationGrounding || bCite.friction == nil || frictionCalls != 1 || st.closeoutRetry.attempt != 3 {
					t.Fatalf("citation refusal: budget=%+v attempt=%d frictionCalls=%d",
						bCite, st.closeoutRetry.attempt, frictionCalls)
				}

				sigs = append(sigs, sig{
					embedRetryAttempt:     embedAttempt,
					embedFrictionCalls:    embedFriction,
					embedKick:             bEmbed.kick,
					imageRetryAttempt:     imageAttempt,
					imageFrictionCalls:    imageFriction,
					citationRetryAttempt:  st.closeoutRetry.attempt,
					citationFrictionCalls: frictionCalls,
				})
			})
		}
		for i := 1; i < len(sigs); i++ {
			if sigs[i] != sigs[0] {
				t.Fatalf("D5 wording divergence on G04-10: %+v vs %+v", sigs[i], sigs[0])
			}
		}
	})
}
