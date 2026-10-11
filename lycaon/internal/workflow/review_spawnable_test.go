package workflow

import (
	"context"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
	runstate "github.com/lycaon/lycaon/internal/workflow/runstate"
	"strings"
	"testing"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func TestReviewLoopDeclaredAgentsUnionsRoles(t *testing.T) {
	got := runstate.ReviewLoopDeclaredAgents(workflowdef.ReviewLoopDef{
		RequiredAgents: []string{"skeptic", "skeptic"},
		IfSpawnable:    []string{"web-researcher", "skeptic"},
	})
	if len(got) != 2 || got[0] != "skeptic" || got[1] != "web-researcher" {
		t.Fatalf("declared = %v want [skeptic web-researcher]", got)
	}
}

func TestReviewLoopFanoutExcludedAgentsFromLaterPhases(t *testing.T) {
	m := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID: "x", Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{
			{ID: "plan", Gates: []string{"fanout_planned"}, Next: "claims"},
			{ID: "claims", ReviewLoop: &workflowdef.ReviewLoopDef{EvidenceKey: "survey_claims"}, Next: "challenge"},
			{ID: "challenge", ReviewLoop: &workflowdef.ReviewLoopDef{
				EvidenceKey:    "survey_challenged",
				RequiredAgents: []string{"skeptic"},
				IfSpawnable:    []string{"web-researcher"},
			}, Next: "done"},
			{ID: "done", Terminal: true, CompleteWhen: "orchestration_complete"},
		},
	})
	got := runstate.ReviewLoopFanoutExcludedAgents(m)
	if len(got) != 2 || got[0] != "skeptic" || got[1] != "web-researcher" {
		t.Fatalf("excluded = %v want [skeptic web-researcher]", got)
	}
}

func TestReviewIfSpawnableSnapshotRoundTrip(t *testing.T) {
	vars := runstate.StampReviewIfSpawnable(nil, "challenge", []string{"web-researcher", "", "web-researcher"})
	got, stamped := runstate.ReviewIfSpawnableSnapshot(vars, "challenge")
	if !stamped || len(got) != 1 || got[0] != "web-researcher" {
		t.Fatalf("snapshot = %v stamped=%v", got, stamped)
	}
	empty := runstate.StampReviewIfSpawnable(nil, "challenge", nil)
	got, stamped = runstate.ReviewIfSpawnableSnapshot(empty, "challenge")
	if !stamped {
		t.Fatal("empty snapshot must still be stamped")
	}
	if len(got) != 0 {
		t.Fatalf("empty snapshot = %v", got)
	}
	if _, ok := runstate.ReviewIfSpawnableSnapshot(nil, "challenge"); ok {
		t.Fatal("missing snapshot must not report stamped")
	}
}

func TestReviewVerdictFromVarsRoundTrip(t *testing.T) {
	vars := runstate.StampReviewVerdict(nil, "survey_claims", map[string]string{
		"verdict":      "CLAIMED",
		"threat_model": "CLI; local user",
		"claims":       "1. stdin parser has no bound",
	})
	got := runstate.ReviewVerdictFromVars(vars, "survey_claims")
	if got["verdict"] != "CLAIMED" || !strings.Contains(got["claims"], "stdin") {
		t.Fatalf("verdict = %+v", got)
	}
}

func TestEffectiveReviewAgentsUsesSnapshot(t *testing.T) {
	rl := workflowdef.ReviewLoopDef{
		RequiredAgents: []string{"skeptic"},
		IfSpawnable:    []string{"web-researcher"},
	}
	if got, captured := runstate.EffectiveReviewAgents("challenge", rl, nil); captured || got != nil {
		t.Fatalf("missing snapshot became roster: %v, %v", got, captured)
	}
	for _, tc := range []struct {
		roster []string
		count  int
	}{
		{nil, 1},
		{[]string{"web-researcher"}, 2},
	} {
		vars := runstate.StampReviewIfSpawnable(nil, "challenge", tc.roster)
		got, captured := runstate.EffectiveReviewAgents("challenge", rl, vars)
		if !captured || len(got) != tc.count || got[0] != "skeptic" {
			t.Fatalf("captured roster = %v, %v", got, captured)
		}
	}
}

func TestReviewIfSpawnableSnapshotRejectsMalformedRoster(t *testing.T) {
	for _, value := range []any{true, "web-researcher", []any{"web-researcher", 7}, []string{""}} {
		vars := map[string]any{"review_if_spawnable": map[string]any{"challenge": value}}
		if got, captured := runstate.ReviewIfSpawnableSnapshot(vars, "challenge"); captured || got != nil {
			t.Fatalf("malformed roster %v became %v, %v", value, got, captured)
		}
	}
}

func TestApplyPhaseOnEnterCapturesReviewerRoster(t *testing.T) {
	manifest := ifSpawnableReviewManifest()
	for _, enabled := range []bool{false, true} {
		vars, err := workflowphases.ApplyPhaseOnEnter(context.Background(), workflowphases.PhaseEnterRequest{
			Manifest: manifest, PhaseID: "challenge", SessionID: "session",
			ReviewSpawnFilter: func(_ context.Context, sessionID, _ string, candidates []string) []string {
				if sessionID != "session" {
					t.Fatalf("filter session = %q", sessionID)
				}
				if enabled {
					return candidates
				}
				return nil
			},
		})
		if err != nil {
			t.Fatalf("phase entry: %v", err)
		}
		roster, captured := runstate.ReviewIfSpawnableSnapshot(vars, "challenge")
		if !captured || (len(roster) == 1) != enabled {
			t.Fatalf("enabled=%v: roster=%v, captured=%v", enabled, roster, captured)
		}
	}
}
