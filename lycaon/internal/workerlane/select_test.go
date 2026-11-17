package workerlane

import "testing"

func TestSelectFollowsTheWorkspaceTable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		facts Facts
		want  Selection
	}{
		{"read round", Facts{}, Selection{Action: ActionSource}},
		{"first write round", Facts{Write: true}, Selection{Action: ActionPrepare}},
		{"stacked first round", Facts{Write: true, BaseCheckpoint: "cp-base"}, Selection{Action: ActionPrepare, FromCheckpoint: "cp-base"}},
		{"corrections keep the lane", Facts{Write: true, HasLane: true}, Selection{Action: ActionKeep}},
		{"shelved keeps the lane", Facts{Write: true, HasLane: true, LastChange: ChangeShelved}, Selection{Action: ActionKeep}},
		{"landed refreshes", Facts{Write: true, HasLane: true, LastChange: ChangeLanded}, Selection{Action: ActionRefresh}},
		{"refreshed lane is kept", Facts{Write: true, HasLane: true, LastChange: ChangeLanded, RefreshDone: true}, Selection{Action: ActionKeep}},
		{"rejected resets to the prior result", Facts{Write: true, HasLane: true, LastChange: ChangeRejected, PriorCheckpoint: "cp-1"}, Selection{Action: ActionReset, FromCheckpoint: "cp-1"}},
		{"rejected first result resets to source", Facts{Write: true, HasLane: true, LastChange: ChangeRejected}, Selection{Action: ActionReset}},
		{"reviewer gets its subject", Facts{Write: true, HasLane: true, Reviewer: true, SubjectCheckpoint: "cp-r"}, Selection{Action: ActionSubject, FromCheckpoint: "cp-r"}},
		{"recovery holds", Facts{Write: true, HasLane: true, RecoveryRequired: true, LastChange: ChangeLanded}, Selection{Action: ActionHold, Hold: HoldRecovery}},
		{"live holders hold", Facts{Write: true, HasLane: true, HoldersUnresolved: true}, Selection{Action: ActionHold, Hold: HoldUnresolvedHolders}},
		{"pending landing holds", Facts{Write: true, HasLane: true, PromotionUnsettled: true}, Selection{Action: ActionHold, Hold: HoldPromotion}},
		{"holds apply to reviewers too", Facts{Reviewer: true, RecoveryRequired: true}, Selection{Action: ActionHold, Hold: HoldRecovery}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Select(tc.facts); got != tc.want {
				t.Fatalf("Select(%+v) = %+v, want %+v", tc.facts, got, tc.want)
			}
		})
	}
}
