package events

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

// Live progress carries each running activity's card, without touching the
// event the caller passed in.
func TestActivityEventsDescribeRunningCommands(t *testing.T) {
	program := &api.CommandProgramReceipt{ID: "prog", Activities: []api.CommandActivity{{ID: "a", Label: "Run process", Subject: "npm run dev", State: "running", JobHandle: "job-1"}}}
	in := api.ActivityEvent{ActivityID: "x", ToolName: "command", ToolCallID: "c", Progress: &api.ToolProgress{CommandProgram: program}}
	out := describeActivityProgram(in)
	action := out.Progress.CommandProgram.Activities[0].Action
	if action == nil || action.Status != "running" || action.Ran.Text != "npm run dev" || action.Opener == nil || action.Opener.ProcessHandle != "job-1" {
		t.Fatalf("running card %+v", action)
	}
	if program.Activities[0].Action != nil {
		t.Fatal("the caller's program was changed")
	}
	if plain := describeActivityProgram(api.ActivityEvent{ActivityID: "y"}); plain.Progress != nil {
		t.Fatalf("an event without progress gained one: %+v", plain)
	}
}
