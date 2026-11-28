package coordgrounding

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/grounding"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// TestAdvisoryModesNeverRefuseAndFailedOutcomesCountedInvariant proves GB10 I5
// (first cases: GB07-F2 repair-exhausted host-assembled closeouts, plus the
// #2909 near-miss that blocked turns in flag mode):
//   - In advisory modes ("warn", "flag"), AfterPrompt never refuses and
//     IsEscalated never reports blocked, even after crossing thresholds.
//   - Each typed closeout outcome class moves breaker counters by its
//     table-declared delta (+1 for repair-exhausted host-assembled closeouts,
//     0 consecutive after a clean traced turn, 0 for immediate host attach).
//   - In "block" mode, crossing the threshold refuses with ErrGroundingEscalated
//     and sets IsEscalated true.
func TestAdvisoryModesNeverRefuseAndFailedOutcomesCountedInvariant(t *testing.T) {
	type outcomeClass struct {
		name             string
		grounding        *api.CitationGrounding
		wantTotalDelta   int
		wantConsecAfter  int
		wantResetsConsec bool
	}

	outcomeClasses := []outcomeClass{
		{
			name: "repair_exhausted_host_assembled_gb07_f2",
			grounding: &api.CitationGrounding{
				Traced:        true,
				HostAssembled: true,
				HintCode:      guidance.InvestCitationUnverifiableCode,
				Checks: []api.CitationGroundingCheck{{
					ID:     "citations",
					Status: api.CitationGroundingCheckStatusFailed,
				}},
			},
			wantTotalDelta:  1,
			wantConsecAfter: 1,
		},
		{
			name: "clean_traced_closeout_resets_streak",
			grounding: &api.CitationGrounding{
				Traced:        true,
				HostAssembled: false,
			},
			wantTotalDelta:   0,
			wantResetsConsec: true,
		},
		{
			name: "immediate_host_attach_without_repair_zero_charge",
			grounding: &api.CitationGrounding{
				Traced:        true,
				HostAssembled: true,
			},
			wantTotalDelta:  0,
			wantConsecAfter: 0,
		},
	}

	modes := []struct {
		mode      string
		mayRefuse bool
	}{
		{mode: "warn", mayRefuse: false},
		{mode: "flag", mayRefuse: false},
		{mode: "block", mayRefuse: true},
	}

	for _, mc := range modes {
		t.Run("mode_"+mc.mode, func(t *testing.T) {
			ctx := t.Context()
			repos := storetest.New(t, testdbseed.DefaultProjectID)
			host := session.NewHost(repos, session.Models{}, nil)
			sess, err := repos.Sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "Create session", err)

			cfg := grounding.DefaultGroundingConfig()
			cfg.CircuitBreaker.EscalateMode = mc.mode
			cfg.CircuitBreaker.MaxConsecutiveUngrounded = 2
			cfg.CircuitBreaker.MaxUngroundedWarnings = 3
			coord := NewAmbientGroundingCoordinator(nil, nil, cfg, nil, host)
			coord.Pipeline = groundingTestPipeline(t)
			coord.Pipeline.SetAdvisorySink(func(_ context.Context, _ string, _ string, _ *oar.Decision) error {
				return nil
			})

			// Verify each typed outcome class moves breaker counters as declared.
			for _, oc := range outcomeClasses {
				before := coord.State.Get(sess.ID)
				if oc.wantResetsConsec {
					// Seed a non-zero streak to verify clean closeout resets it.
					seed := before
					seed.ConsecutiveWarnings = 1
					coord.State.Set(sess.ID, seed)
					before = seed
				}
				testutil.FailErr(t, "append assistant message "+oc.name, repos.Transcript.AppendMessages(ctx, sess.ID, api.Message{
					Role:      api.MessageRoleAssistant,
					Content:   "closeout " + oc.name,
					Grounding: oc.grounding,
				}))
				err := coord.AfterPrompt(ctx, sess.ID, nil)
				if !mc.mayRefuse && err != nil {
					t.Fatalf("advisory mode %q refused turn on %s: %v", mc.mode, oc.name, err)
				}
				after := coord.State.Get(sess.ID)
				if gotDelta := after.TotalWarnings - before.TotalWarnings; gotDelta != oc.wantTotalDelta {
					t.Errorf("mode=%s outcome=%s total delta=%d, want %d (before=%+v after=%+v)",
						mc.mode, oc.name, gotDelta, oc.wantTotalDelta, before, after)
				}
				if oc.wantResetsConsec && after.ConsecutiveWarnings != 0 {
					t.Errorf("mode=%s outcome=%s did not reset consecutive streak: %+v", mc.mode, oc.name, after)
				}
				if !mc.mayRefuse && coord.IsEscalated(sess.ID) {
					t.Fatalf("advisory mode %q set IsEscalated=true on %s", mc.mode, oc.name)
				}
			}

			// Drive consecutive repair-exhausted closeouts past the threshold.
			coord.Reset(sess.ID)
			exhausted := outcomeClasses[0].grounding
			var lastErr error
			for turn := range cfg.CircuitBreaker.MaxConsecutiveUngrounded + 1 {
				testutil.FailErr(t, "append exhausted closeout", repos.Transcript.AppendMessages(ctx, sess.ID, api.Message{
					Role:      api.MessageRoleAssistant,
					Content:   "exhausted closeout",
					Grounding: exhausted,
				}))
				lastErr = coord.AfterPrompt(ctx, sess.ID, nil)
				if !mc.mayRefuse {
					if lastErr != nil {
						t.Fatalf("advisory mode %q refused turn %d after threshold: %v", mc.mode, turn+1, lastErr)
					}
					if coord.IsEscalated(sess.ID) {
						t.Fatalf("advisory mode %q reported IsEscalated=true on turn %d (near-miss #2909)", mc.mode, turn+1)
					}
				}
			}
			if mc.mayRefuse {
				if !errors.Is(lastErr, guidance.ErrGroundingEscalated) || !coord.IsEscalated(sess.ID) {
					t.Fatalf("block mode did not refuse after crossing threshold: err=%v escalated=%v state=%+v",
						lastErr, coord.IsEscalated(sess.ID), coord.State.Get(sess.ID))
				}
			}

			// Even if StateStore.Escalated is manually set, advisory modes MUST NEVER
			// report IsEscalated=true (#2909 near-miss).
			if !mc.mayRefuse {
				coord.State.Set(sess.ID, grounding.UngroundedCounter{
					TotalWarnings:       cfg.CircuitBreaker.MaxUngroundedWarnings,
					ConsecutiveWarnings: cfg.CircuitBreaker.MaxConsecutiveUngrounded,
					Escalated:           true,
				})
				if coord.IsEscalated(sess.ID) {
					t.Fatalf("advisory mode %q reported IsEscalated=true when state.Escalated was set", mc.mode)
				}
			}
		})
	}
}
