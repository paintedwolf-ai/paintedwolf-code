package bgprocess

import (
	"reflect"
	"testing"
)

func TestBackgroundCapacitySnapshotTracksOnlyAdmittedLane(t *testing.T) {
	r := &processTable{maxBackground: 7, sessions: map[string]map[string]*Process{
		"session": {
			"terminal": {Handle: "terminal", kind: processKindPTY, mode: JobModeBackground, running: true},
			"command":  {Handle: "command", mode: JobModeBackground, running: true},
			"finished": {Handle: "finished", mode: JobModeBackground},
			"awaited":  {Handle: "awaited", mode: JobModeAwaited, running: true},
		},
		"other": {"foreign": {Handle: "foreign", mode: JobModeBackground, running: true}},
	}}
	snapshot := r.backgroundCapacityErrorLocked("session")
	if snapshot.Limit != 7 || !reflect.DeepEqual(snapshot.TerminalIDs, []string{"terminal"}) || !reflect.DeepEqual(snapshot.CommandHandles, []string{"command"}) {
		t.Fatalf("capacity snapshot = %+v", snapshot)
	}
	if len(snapshot.TerminalIDs)+len(snapshot.CommandHandles) != r.countRunningBackgroundLocked("session") {
		t.Fatal("recovery snapshot disagrees with admission count")
	}
	r.sessions["session"]["terminal"].running = false
	if !reflect.DeepEqual(snapshot.TerminalIDs, []string{"terminal"}) {
		t.Fatal("later completion changed the refused admission snapshot")
	}
}

func TestAwaitedCapacitySnapshotTracksConfiguredLane(t *testing.T) {
	r := &processTable{maxAwaited: 2, sessions: map[string]map[string]*Process{
		"session": {
			"a":      {Handle: "a", mode: JobModeAwaited, running: true},
			"b":      {Handle: "b", mode: JobModeAwaited, running: true},
			"done":   {Handle: "done", mode: JobModeAwaited},
			"server": {Handle: "server", mode: JobModeBackground, running: true},
		},
		"other": {"foreign": {Handle: "foreign", mode: JobModeAwaited, running: true}},
	}}
	snapshot := r.awaitedCapacityErrorLocked("session")
	if snapshot.Limit != 2 || !reflect.DeepEqual(snapshot.Handles, []string{"a", "b"}) || len(snapshot.Handles) != r.countRunningAwaitedLocked("session") {
		t.Fatalf("admission snapshot disagrees with configured lane: %+v", snapshot)
	}
	r.sessions["session"]["a"].running = false
	if !reflect.DeepEqual(snapshot.Handles, []string{"a", "b"}) {
		t.Fatalf("completion mutated frozen admission: %+v", snapshot)
	}
}
