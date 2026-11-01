package events

import (
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestActivitySnapshotsDetachCommandMembershipAndOutcomes(t *testing.T) {
	zero := 0
	original := api.ActivityEvent{Progress: &api.ToolProgress{CommandProgram: &api.CommandProgramReceipt{
		Activities: []api.CommandActivity{{ExecutionIDs: []string{"execution"}, Spans: []api.CommandActivitySpan{{SourceRef: "source"}}, StatusCode: &zero, Steps: []api.CommandActivityStep{{ID: "step", Count: 1}}}},
	}}}
	snapshot := cloneActivity(original)
	snapshot.Progress.CommandProgram.Activities[0].ExecutionIDs[0] = "changed"
	snapshot.Progress.CommandProgram.Activities[0].Spans[0].SourceRef = "changed"
	snapshot.Progress.CommandProgram.Activities[0].Steps[0].Count = 2
	*snapshot.Progress.CommandProgram.Activities[0].StatusCode = 1
	if original.Progress.CommandProgram.Activities[0].ExecutionIDs[0] != "execution" || original.Progress.CommandProgram.Activities[0].Spans[0].SourceRef != "source" || original.Progress.CommandProgram.Activities[0].Steps[0].Count != 1 || zero != 0 {
		t.Fatal("snapshot shared command activity state")
	}
	if cloneCommandActivityProgram(nil) != nil || cloneActivityStatus(nil) != nil {
		t.Fatal("nil snapshot acquired state")
	}
}
