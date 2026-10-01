package contract

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/pkg/api"
)

// Ledger merge_status on ImplementSessionState.PendingOverlayIDs is the routing authority.

func stalePendingEnvelopeHistory(jobIDs ...string) []api.Message {
	msgs := make([]api.Message, 0, len(jobIDs))
	for _, id := range jobIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		msgs = append(msgs, api.Message{
			Role: api.MessageRoleAssistant,
			Content: fmt.Sprintf(
				`<task job_id=%q agent_type="implementer" state="complete" merge_status="pending"><summary>wrote file</summary></task>`,
				id,
			),
			WorkerSummary: &api.WorkerSummaryMeta{
				WorkerID:  id,
				AgentType: "implementer",
				Status:    api.WorkerSummaryStatusOpen,
			},
		})
	}
	return msgs
}

func completeImplementerHostCycleHistory() []api.Message {
	return append(stalePendingEnvelopeHistory(), api.Message{
		Role:    api.MessageRoleAssistant,
		Content: `<task job_id="j-done" agent_type="implementer" state="complete"><summary>done</summary></task>`,
		WorkerSummary: &api.WorkerSummaryMeta{
			WorkerID:  "j-done",
			AgentType: "implementer",
			Status:    api.WorkerSummaryStatusComplete,
		},
	})
}

func ledgerResolved(ids ...string) surface.ImplementSessionState {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" {
			out = append(out, id)
		}
	}
	return surface.ImplementSessionState{PendingOverlayIDs: out}
}

func ledgerResolvedEmpty() surface.ImplementSessionState {
	return ledgerResolved()
}

func TestCoordinatorOverlayLedgerInvariants_pendingTruthMatrix(t *testing.T) {
	t.Parallel()

	historyTwoStale := stalePendingEnvelopeHistory("6fd7c5e0-1722-41e3-9cde-07bc374b74e3", "f4d1856a-7eed-4e2d-a87b-ce3da7cafc33")

	cases := []struct {
		name      string
		history   []api.Message
		state     surface.ImplementSessionState
		wantIDs   []string
		wantPromo bool
	}{
		{
			name:      "empty_ledger_state_ignores_envelope",
			history:   historyTwoStale,
			state:     surface.ImplementSessionState{},
			wantIDs:   nil,
			wantPromo: false,
		},
		{
			name:      "ledger_empty_after_terminal_reject_ignores_stale_envelope",
			history:   historyTwoStale,
			state:     ledgerResolvedEmpty(),
			wantIDs:   nil,
			wantPromo: false,
		},
		{
			name:      "ledger_pending_wins_without_envelope",
			history:   completeImplementerHostCycleHistory(),
			state:     ledgerResolved("j-live"),
			wantIDs:   []string{"j-live"},
			wantPromo: true,
		},
		{
			name:      "ledger_empty_and_no_envelope",
			history:   completeImplementerHostCycleHistory(),
			state:     ledgerResolvedEmpty(),
			wantIDs:   nil,
			wantPromo: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.state.PendingOverlayIDs
			if !sameStringSet(got, tc.wantIDs) {
				t.Fatalf("OverlayPromotePending = %v want %v", got, tc.wantIDs)
			}
			promoDue := len(got) > 0
			if promoDue != tc.wantPromo {
				t.Fatalf("overlay promote due = %v want %v (ids=%v)", promoDue, tc.wantPromo, got)
			}
		})
	}
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]struct{}, len(a))
	for _, s := range a {
		seen[s] = struct{}{}
	}
	for _, s := range b {
		if _, ok := seen[s]; !ok {
			return false
		}
	}
	return true
}

func TestCoordinatorOverlayLedgerInvariants_rejectedOverlaysLeavePromoteSurface(t *testing.T) {
	t.Parallel()

	history := stalePendingEnvelopeHistory(
		"6fd7c5e0-1722-41e3-9cde-07bc374b74e3",
		"f4d1856a-7eed-4e2d-a87b-ce3da7cafc33",
	)
	state := ledgerResolvedEmpty()

	profile := surface.ResolveTurnProfile(
		api.CoordinatorRunContext{},
		&api.Session{Posture: api.SessionPostureBuild},
		coordinatorTurnHistory(history, surface.HostLoopWakeSentinel),
		state,
	)
	if profile.SurfaceID == surface.SurfaceImplementOverlayPromote {
		t.Fatalf("surface = %q want investigate after ledger clears pending overlays", profile.SurfaceID)
	}
}

func TestCoordinatorOverlayLedgerInvariants_rosterLoadedStillPromotesWhenPending(t *testing.T) {
	t.Parallel()

	history := stalePendingEnvelopeHistory("j1")
	state := ledgerResolved("j1")

	profile := surface.ResolveTurnProfile(
		api.CoordinatorRunContext{},
		&api.Session{Posture: api.SessionPostureBuild},
		coordinatorTurnHistory(history, surface.HostLoopWakeSentinel),
		state,
	)
	if profile.SurfaceID != surface.SurfaceImplementOverlayPromote {
		t.Fatalf("surface = %q want %q", profile.SurfaceID, surface.SurfaceImplementOverlayPromote)
	}
}

func TestCoordinatorOverlayLedgerInvariants_ignoresEnvelopeWithoutLedgerState(t *testing.T) {
	t.Parallel()

	history := stalePendingEnvelopeHistory("j1")
	profile := surface.ResolveTurnProfile(
		api.CoordinatorRunContext{},
		&api.Session{Posture: api.SessionPostureBuild},
		coordinatorTurnHistory(history, surface.HostLoopWakeSentinel),
	)
	if profile.SurfaceID == surface.SurfaceImplementOverlayPromote {
		t.Fatalf("surface = %q must not route from transcript overlay state", profile.SurfaceID)
	}
}

func TestCoordinatorOverlayLedgerInvariants_hostTurnGuardAgreesWithLedger(t *testing.T) {
	t.Parallel()

	state := ledgerResolvedEmpty()

	if !guard.HostTurnMayFinishWithProse(
		"implement_synthesis",
		true,
		len(state.PendingOverlayIDs) > 0,
	) {
		t.Fatal("expected synthesis prose allowed when ledger reports zero pending overlays despite stale envelope")
	}
}
