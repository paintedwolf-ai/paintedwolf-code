package invocation

import "testing"

func TestActivityContainerFallbackFollowsAssignedDescendants(t *testing.T) {
	outer := activityFixture("outer", "root", "program |", "", 0)
	inner := activityFixture("inner", "outer", "program ( )", "", 2)
	orphan := activityFixture("leaf", "inner", "read", "head", 4)

	for _, tt := range []struct {
		name   string
		inputs []activityInput
		want   string
	}{
		{"empty pipeline", []activityInput{outer}, "outer"},
		{"empty nested container", []activityInput{outer, inner}, "inner"},
		{"failed supporting leaf", []activityInput{outer, inner, orphan}, "leaf"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			groups := activityGroups(tt.inputs...)
			for i := range groups {
				groups[i].counts.failed = 1
			}
			got := composeActivityGroups(groups, activityProgramSettled)
			if len(got) != 1 || got[0].ID != tt.want || got[0].FailedCount != 1 {
				t.Fatalf("activity=%+v, want only failed %s", got, tt.want)
			}
		})
	}
}

func TestRecoveredSupportingActivityKeepsItsRow(t *testing.T) {
	contextStep := activityFixture("context", "root", "cd", "cd", 0)
	groups := activityGroups(contextStep)
	groups[0].counts.failed = 1
	groups[0].counts.utilityFailures = 1
	before := composeActivityGroups(groups, activityProgramRunning)
	after := composeActivityGroups(groups, activityProgramSucceeded)
	if len(before) != 1 || len(after) != 1 || before[0].ID != after[0].ID || before[0].State != "failed" || after[0].State != "completed" || after[0].FailedCount != 0 {
		t.Fatalf("recovered row: before=%+v after=%+v", before, after)
	}
}
