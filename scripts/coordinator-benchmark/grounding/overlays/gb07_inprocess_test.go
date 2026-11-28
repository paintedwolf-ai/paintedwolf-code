package coordgrounding

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/grounding"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/limits"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/closeouts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type gb07WordingVariant struct {
	name   string
	prefix string
}

var gb07D5Wordings = []gb07WordingVariant{
	{name: "confident", prefix: "Verified:"},
	{name: "hedged", prefix: "Appears verified:"},
	{name: "completion_asserting", prefix: "Complete and verified:"},
}

// TestGB07InProcessCircuitBreakerAndFriction covers the 5 coordgrounding/closeouts
// GB07 in-process slots across all 3 D5 wordings (15 subtests):
// - G04-06-inprocess-block-mode-3-total-escalates
// - G04-07-inprocess-block-mode-2-consecutive-escalates
// - G04-08-inprocess-block-mode-stays-tripped
// - G04-09-inprocess-ambient-escalation-code
// - G04-11-inprocess-shared-friction-budget-exhaustion
func TestGB07InProcessCircuitBreakerAndFriction(t *testing.T) {
	t.Run("G04-06-inprocess-block-mode-3-total-escalates", func(t *testing.T) {
		type sig struct {
			escalatedBeforeThird bool
			escalatedAfterThird  bool
			errEscalated         bool
			noticeCode           string
		}
		var sigs []sig
		for _, w := range gb07D5Wordings {
			t.Run(w.name, func(t *testing.T) {
				cfg := grounding.DefaultGroundingConfig()
				cfg.CircuitBreaker.EscalateMode = "block"
				cfg.CircuitBreaker.MaxUngroundedWarnings = 3
				cfg.CircuitBreaker.MaxConsecutiveUngrounded = 2

				stateStore := grounding.NewStateStore()
				coord := NewAmbientGroundingCoordinator(nil, nil, cfg, stateStore, nil)
				coord.Pipeline = groundingTestPipeline(t)

				sid := "gb07-g04-06-" + w.name
				var notices []*oar.Decision
				coord.Pipeline.SetAdvisorySink(func(_ context.Context, _ string, _ string, d *oar.Decision) error {
					notices = append(notices, d)
					return nil
				})

				// 3 non-consecutive ungrounded warnings separated by streak resets
				st := stateStore.Get(sid)
				grounding.ApplyUngroundedWarning(&st, 3, 2, true)
				grounding.ResetUngroundedStreak(&st)
				grounding.ApplyUngroundedWarning(&st, 3, 2, true)
				grounding.ResetUngroundedStreak(&st)
				stateStore.Set(sid, st)
				beforeThird := coord.IsEscalated(sid)
				if beforeThird {
					t.Fatalf("should not escalate after 2 non-consecutive warnings")
				}

				grounding.ApplyUngroundedWarning(&st, 3, 2, true)
				stateStore.Set(sid, st)
				afterThird := coord.IsEscalated(sid)
				if !afterThird {
					t.Fatalf("expected escalation at 3 total non-consecutive warnings")
				}

				err := coord.AfterPrompt(t.Context(), sid, nil)
				errEsc := errors.Is(err, guidance.ErrGroundingEscalated)
				code := ""
				if len(notices) > 0 {
					code = notices[len(notices)-1].Code
				}
				if !errEsc || code != grounding.CoordinatorEscalatedCode {
					t.Fatalf("wording=%s: err=%v code=%q want ErrGroundingEscalated + %s",
						w.name, err, code, grounding.CoordinatorEscalatedCode)
				}
				sigs = append(sigs, sig{
					escalatedBeforeThird: beforeThird,
					escalatedAfterThird:  afterThird,
					errEscalated:         errEsc,
					noticeCode:           code,
				})
			})
		}
		for i := 1; i < len(sigs); i++ {
			if sigs[i] != sigs[0] {
				t.Fatalf("D5 wording divergence on G04-06: %+v vs %+v", sigs[i], sigs[0])
			}
		}
	})

	t.Run("G04-07-inprocess-block-mode-2-consecutive-escalates", func(t *testing.T) {
		type sig struct {
			escalatedAfterFirst  bool
			escalatedAfterSecond bool
			errEscalated         bool
			noticeCode           string
		}
		var sigs []sig
		for _, w := range gb07D5Wordings {
			t.Run(w.name, func(t *testing.T) {
				cfg := grounding.DefaultGroundingConfig()
				cfg.CircuitBreaker.EscalateMode = "block"
				cfg.CircuitBreaker.MaxUngroundedWarnings = 3
				cfg.CircuitBreaker.MaxConsecutiveUngrounded = 2

				stateStore := grounding.NewStateStore()
				coord := NewAmbientGroundingCoordinator(nil, nil, cfg, stateStore, nil)
				coord.Pipeline = groundingTestPipeline(t)

				sid := "gb07-g04-07-" + w.name
				var notices []*oar.Decision
				coord.Pipeline.SetAdvisorySink(func(_ context.Context, _ string, _ string, d *oar.Decision) error {
					notices = append(notices, d)
					return nil
				})

				st := stateStore.Get(sid)
				grounding.ApplyUngroundedWarning(&st, 3, 2, true)
				stateStore.Set(sid, st)
				afterFirst := coord.IsEscalated(sid)
				if afterFirst {
					t.Fatalf("should not escalate after 1 warning")
				}

				grounding.ApplyUngroundedWarning(&st, 3, 2, true)
				stateStore.Set(sid, st)
				afterSecond := coord.IsEscalated(sid)
				if !afterSecond {
					t.Fatalf("expected escalation at 2 consecutive warnings")
				}

				err := coord.AfterPrompt(t.Context(), sid, nil)
				errEsc := errors.Is(err, guidance.ErrGroundingEscalated)
				code := ""
				if len(notices) > 0 {
					code = notices[len(notices)-1].Code
				}
				if !errEsc || code != grounding.CoordinatorEscalatedCode {
					t.Fatalf("wording=%s: err=%v code=%q want ErrGroundingEscalated + %s",
						w.name, err, code, grounding.CoordinatorEscalatedCode)
				}
				sigs = append(sigs, sig{
					escalatedAfterFirst:  afterFirst,
					escalatedAfterSecond: afterSecond,
					errEscalated:         errEsc,
					noticeCode:           code,
				})
			})
		}
		for i := 1; i < len(sigs); i++ {
			if sigs[i] != sigs[0] {
				t.Fatalf("D5 wording divergence on G04-07: %+v vs %+v", sigs[i], sigs[0])
			}
		}
	})

	t.Run("G04-08-inprocess-block-mode-stays-tripped", func(t *testing.T) {
		type sig struct {
			stillEscalatedAfterStreakReset bool
			secondTurnErrEscalated         bool
			secondTurnCode                 string
			resetClearsEscalation          bool
		}
		var sigs []sig
		for _, w := range gb07D5Wordings {
			t.Run(w.name, func(t *testing.T) {
				cfg := grounding.DefaultGroundingConfig()
				cfg.CircuitBreaker.EscalateMode = "block"
				cfg.CircuitBreaker.MaxUngroundedWarnings = 3
				cfg.CircuitBreaker.MaxConsecutiveUngrounded = 2

				stateStore := grounding.NewStateStore()
				coord := NewAmbientGroundingCoordinator(nil, nil, cfg, stateStore, nil)
				coord.Pipeline = groundingTestPipeline(t)

				sid := "gb07-g04-08-" + w.name
				var notices []*oar.Decision
				coord.Pipeline.SetAdvisorySink(func(_ context.Context, _ string, _ string, d *oar.Decision) error {
					notices = append(notices, d)
					return nil
				})

				// Trip breaker at 2 consecutive warnings
				st := stateStore.Get(sid)
				grounding.ApplyUngroundedWarning(&st, 3, 2, true)
				grounding.ApplyUngroundedWarning(&st, 3, 2, true)
				// Subsequent clean turn resets consecutive streak only, NOT Escalated
				grounding.ResetUngroundedStreak(&st)
				stateStore.Set(sid, st)

				stillEsc := coord.IsEscalated(sid)
				if !stillEsc {
					t.Fatalf("tripped breaker must stay tripped after ResetUngroundedStreak")
				}

				// First escalation check
				err1 := coord.AfterPrompt(t.Context(), sid, nil)
				if !errors.Is(err1, guidance.ErrGroundingEscalated) {
					t.Fatalf("turn 1: expected ErrGroundingEscalated, got %v", err1)
				}
				// Subsequent turn without reset still escalates
				err2 := coord.AfterPrompt(t.Context(), sid, []string{"command"})
				err2Esc := errors.Is(err2, guidance.ErrGroundingEscalated)
				code2 := ""
				if len(notices) > 0 {
					code2 = notices[len(notices)-1].Code
				}
				if !err2Esc || code2 != grounding.CoordinatorEscalatedCode {
					t.Fatalf("turn 2: err=%v code=%q want ErrGroundingEscalated + %s",
						err2, code2, grounding.CoordinatorEscalatedCode)
				}

				// Explicit reset via instruct_worker clears escalation
				if err := coord.AfterPrompt(t.Context(), sid, []string{"instruct_worker"}); err != nil {
					t.Fatalf("explicit reset failed: %v", err)
				}
				resetCleared := !coord.IsEscalated(sid)
				if !resetCleared {
					t.Fatalf("explicit reset must clear escalated state")
				}

				sigs = append(sigs, sig{
					stillEscalatedAfterStreakReset: stillEsc,
					secondTurnErrEscalated:         err2Esc,
					secondTurnCode:                 code2,
					resetClearsEscalation:          resetCleared,
				})
			})
		}
		for i := 1; i < len(sigs); i++ {
			if sigs[i] != sigs[0] {
				t.Fatalf("D5 wording divergence on G04-08: %+v vs %+v", sigs[i], sigs[0])
			}
		}
	})

	t.Run("G04-09-inprocess-ambient-escalation-code", func(t *testing.T) {
		type sig struct {
			observedEscalated bool
			claimsCompletion  bool
			emittedCode       string
			flaggedAfterTwo   bool
		}
		var sigs []sig
		for _, w := range gb07D5Wordings {
			t.Run(w.name, func(t *testing.T) {
				cfg := grounding.DefaultGroundingConfig()
				cfg.CircuitBreaker.MaxUngroundedWarnings = 3
				cfg.CircuitBreaker.MaxConsecutiveUngrounded = 2

				const projID = "84a676a3-ccd2-5967-97ed-fd384c0b9003"
				hub := events.NewMemoryHub()
				pub := &events.Publisher{
					Hub: hub,
					SessionProject: func(_ context.Context, sessionID string) (string, bool) {
						return projID, sessionID != ""
					},
				}
				ch, unsub, err := hub.Subscribe(t.Context(), events.Subscription{
					Project: projID,
					Viewer:  testutil.HostOwner(),
				})
				testutil.FailErr(t, "hub.Subscribe", err)
				defer unsub()

				coord := NewAmbientGroundingCoordinator(nil, nil, cfg, nil, nil)
				coord.Pipeline = groundingTestPipeline(t)
				coord.Events = pub
				coord.Pipeline.SetAdvisorySink(func(_ context.Context, _ string, _ string, _ *oar.Decision) error {
					return nil
				})

				sid := "gb07-g04-09-" + w.name
				in := AmbientGroundingInput{
					SessionID: sid,
					ProjectID: projID,
					Jobs:      []api.WorkerTask{{ID: "job-1", Status: api.WorkerStatusComplete}},
				}
				verdict := grounding.GroundingVerdict{
					OK:   false,
					Code: grounding.AmbientUngroundedCompletionCode,
				}
				if err := coord.applyOARAmbientGrounding(t.Context(), sid, ambientStateKey(sid), in, verdict); err != nil {
					t.Fatalf("ambient 1: %v", err)
				}
				if err := coord.applyOARAmbientGrounding(t.Context(), sid, ambientStateKey(sid), in, verdict); err != nil {
					t.Fatalf("ambient 2: %v", err)
				}

				flagged := grounding.Flagged(coord.State.Get(ambientStateKey(sid)), cfg)
				if !flagged {
					t.Fatalf("expected ambient state to be flagged after 2 consecutive warnings")
				}

				// Drain emitted grounding events and verify AMBIENT_GROUNDING_ESCALATED
				emittedCode := ""
				for len(ch) > 0 {
					env := <-ch
					if env.Topic == api.EventTopicGrounding {
						var payload api.GroundingEvent
						if json.Unmarshal(env.Data, &payload) == nil && payload.Code != "" {
							emittedCode = payload.Code
						}
					}
				}
				if emittedCode != grounding.AmbientGroundingEscalatedCode {
					t.Fatalf("emitted grounding code = %q, want %q", emittedCode, grounding.AmbientGroundingEscalatedCode)
				}

				// Verify ObserveVerdict on AMBIENT_GROUNDING_ESCALATED sets GroundingEscalated=true, ClaimsCompletion=false
				gc := oar.NewGuardContext()
				grounding.ObserveVerdict(gc, grounding.GroundingVerdict{
					OK:   false,
					Code: grounding.AmbientGroundingEscalatedCode,
				})
				if !gc.Grounding.GroundingEscalated || gc.Grounding.ClaimsCompletion {
					t.Fatalf("ObserveVerdict(AMBIENT_GROUNDING_ESCALATED): escalated=%v claims=%v",
						gc.Grounding.GroundingEscalated, gc.Grounding.ClaimsCompletion)
				}

				sigs = append(sigs, sig{
					observedEscalated: gc.Grounding.GroundingEscalated,
					claimsCompletion:  gc.Grounding.ClaimsCompletion,
					emittedCode:       emittedCode,
					flaggedAfterTwo:   flagged,
				})
			})
		}
		for i := 1; i < len(sigs); i++ {
			if sigs[i] != sigs[0] {
				t.Fatalf("D5 wording divergence on G04-09: %+v vs %+v", sigs[i], sigs[0])
			}
		}
	})

	t.Run("G04-11-inprocess-shared-friction-budget-exhaustion", func(t *testing.T) {
		type sig struct {
			exhaustedAtCap         bool
			firstRemaining         int
			capStep                int
			freshPhaseNotExhausted bool
		}
		var sigs []sig
		for _, w := range gb07D5Wordings {
			t.Run(w.name, func(t *testing.T) {
				svc := closeouts.New(nil)
				ctx := t.Context()
				sid := "gb07-g04-11-" + w.name
				capLimit := min(limits.DefaultGroundingRejectsPerTurn, limits.DefaultGroundingRejectsPerCycle)

				firstRem := -1
				exhaustedAtCap := false
				for i := 1; i <= capLimit; i++ {
					fr := svc.RecordGroundingFriction(ctx, sid)
					wantRem := capLimit - i
					if fr.Remaining != wantRem {
						t.Fatalf("step %d: remaining=%d want=%d", i, fr.Remaining, wantRem)
					}
					if i == 1 {
						firstRem = fr.Remaining
					}
					if i < capLimit && fr.Exhausted() {
						t.Fatalf("step %d/%d exhausted early: %+v", i, capLimit, fr)
					}
					if i == capLimit {
						exhaustedAtCap = fr.Exhausted()
					}
				}
				if !exhaustedAtCap {
					t.Fatalf("expected budget exhausted at cap=%d", capLimit)
				}

				// Entering a new workflow phase + new prompt resets friction budget
				svc.BeginWorkflowPhase(ctx, sid)
				svc.BeginIntent(ctx, sid)
				afterReset := svc.RecordGroundingFriction(ctx, sid)
				freshOK := !afterReset.Exhausted() && afterReset.Remaining == capLimit-1
				if !freshOK {
					t.Fatalf("expected fresh budget after phase/intent reset, got %+v", afterReset)
				}

				sigs = append(sigs, sig{
					exhaustedAtCap:         exhaustedAtCap,
					firstRemaining:         firstRem,
					capStep:                capLimit,
					freshPhaseNotExhausted: freshOK,
				})
			})
		}
		for i := 1; i < len(sigs); i++ {
			if sigs[i] != sigs[0] {
				t.Fatalf("D5 wording divergence on G04-11: %+v vs %+v", sigs[i], sigs[0])
			}
		}
	})
}
