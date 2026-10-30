package promptloop

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestCommandActivityEdgesBypassCounterCoalescing(t *testing.T) {
	initial := &api.ToolProgress{CommandProgram: &api.CommandProgramReceipt{Activities: []api.CommandActivity{{ID: "first", State: "running", Outcome: "Running"}}}}
	if !commandActivityEdge(nil, initial) {
		t.Fatal("first visible activity was delayed")
	}
	unchanged := *initial
	if commandActivityEdge(initial, &unchanged) {
		t.Fatal("unchanged snapshot bypassed coalescing")
	}
	next := &api.ToolProgress{CommandProgram: &api.CommandProgramReceipt{Activities: []api.CommandActivity{{ID: "first", State: "completed", Outcome: "Exited 0"}}}}
	if !commandActivityEdge(initial, next) {
		t.Fatal("terminal activity was delayed")
	}
	next.CommandProgram.ActivitiesOmitted = 3
	if !commandActivityEdge(initial, next) {
		t.Fatal("first overflow indication was delayed")
	}
	if commandActivityEdge(nil, nil) {
		t.Fatal("missing progress created an edge")
	}
}
