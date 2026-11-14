package workerstate

import (
	"slices"
	"testing"
)

func TestDeriveStatesAndAdmittedOperations(t *testing.T) {
	running := &Round{}
	reported := &Round{Settled: true, Reported: true}
	cases := []struct {
		name   string
		facts  Facts
		state  State
		reason Reason
		ops    []Operation
	}{
		{"closed", Facts{Closed: true, Latest: running}, Closed, ReasonWithdrawn, []Operation{OpHistory}},
		{"unusable session", Facts{SessionUnusable: true}, Closed, ReasonSessionUnusable, []Operation{OpHistory}},
		{"recovering", Facts{Recovery: true, Latest: running}, Recovering, ReasonRecovery, []Operation{OpRecover, OpHistory}},
		{"planned", Facts{Planned: true, Role: RolePlanned}, Planned, ReasonNone, []Operation{OpStart, OpWithdraw, OpHistory}},
		{"waiting", Facts{Latest: &Round{Held: true}}, Waiting, ReasonPrerequisite, []Operation{OpWithdraw, OpHistory}},
		{"asking", Facts{Latest: &Round{DecisionPending: true}}, Asking, ReasonNone, []Operation{OpAnswer, OpCancel, OpHistory}},
		{"working", Facts{Latest: running}, Working, ReasonNone, []Operation{OpInstruct, OpCancel, OpHistory}},
		{"queued", Facts{Latest: &Round{Queued: true}}, Working, ReasonQueued, []Operation{OpInstruct, OpCancel, OpHistory}},
		{"parked", Facts{Latest: &Round{Parked: true}}, Working, ReasonParked, []Operation{OpInstruct, OpCancel, OpHistory}},
		{"approval wait", Facts{Latest: &Round{Parked: true, ApprovalWait: true}}, Working, ReasonApprovalWait, []Operation{OpInstruct, OpCancel, OpHistory}},
		{"delivered unaccounted", Facts{Latest: reported, UnaccountedComplete: true}, Delivered, ReasonUnaccounted,
			[]Operation{OpInstruct, OpReview, OpAccount, OpHistory}},
		{"delivered with changes", Facts{Latest: reported, OpenChanges: true}, Delivered, ReasonChangesOpen,
			[]Operation{OpInstruct, OpPreview, OpPromote, OpReject, OpShelve, OpReview, OpHistory}},
		{"idle", Facts{Latest: reported}, Idle, ReasonNone, []Operation{OpInstruct, OpHistory}},
		{"idle with shelved changes", Facts{Latest: reported, ShelvedChanges: true}, Idle, ReasonNone,
			[]Operation{OpInstruct, OpPreview, OpPromote, OpReject, OpHistory}},
		{"failed keeping changes", Facts{Latest: &Round{Settled: true}, OpenChanges: true}, Failed, ReasonNoReport,
			[]Operation{OpInstruct, OpPreview, OpPromote, OpReject, OpShelve, OpHistory}},
		{"failed without changes", Facts{Latest: &Round{Settled: true}}, Failed, ReasonNoReport, []Operation{OpInstruct, OpHistory}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := Derive(tc.facts)
			if v.State != tc.state || v.Reason != tc.reason || !slices.Equal(v.Ops, tc.ops) {
				t.Fatalf("view = %+v, want %s %q %v", v, tc.state, tc.reason, tc.ops)
			}
		})
	}
}

// A failed round stays failed whatever its earlier results left: the state
// lasts until the next round is admitted.
func TestFailedOutranksEarlierResults(t *testing.T) {
	v := Derive(Facts{Latest: &Round{Settled: true}, UnaccountedComplete: true})
	if v.State != Failed || v.Admits(OpAccount) {
		t.Fatalf("view = %+v", v)
	}
}

func TestAgentChange(t *testing.T) {
	idle := Facts{Latest: &Round{Settled: true, Reported: true}}
	cases := []struct {
		name               string
		facts              Facts
		fromWrite, toWrite bool
		want               AgentChange
	}{
		{"read to write", idle, false, true, AgentChangeAdmitted},
		{"reviewer", Facts{Role: RoleReviewer, Latest: idle.Latest}, false, true, AgentChangeSealed},
		{"plan bound", Facts{Role: RolePlanned, Latest: idle.Latest}, false, false, AgentChangeSealed},
		{"working", Facts{Latest: &Round{}}, false, true, AgentChangeBusy},
		{"asking", Facts{Latest: &Round{DecisionPending: true}}, false, true, AgentChangeBusy},
		{"write to read with open changes", Facts{Latest: idle.Latest, OpenChanges: true}, true, false, AgentChangeOpenChanges},
		{"write to read with shelved changes", Facts{Latest: idle.Latest, ShelvedChanges: true}, true, false, AgentChangeOpenChanges},
		{"write to write with open changes", Facts{Latest: idle.Latest, OpenChanges: true}, true, true, AgentChangeAdmitted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CheckAgentChange(Derive(tc.facts), tc.facts, tc.fromWrite, tc.toWrite); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}
