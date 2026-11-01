package command

import (
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/confine"
)

func TestRenderedRunningTerminalCaptureHasSuccessfulTypedResult(t *testing.T) {
	captured := bgprocess.PTYCaptureResult{
		Screen:   bgprocess.ScreenSnapshot{Cols: 80, Rows: 2, Lines: []string{"board", "your move"}},
		TimedOut: true,
	}
	snapshot := TerminalCaptureSnapshot(captured, "game", terminalScreenText(captured.Screen))
	result := CommandResultFromOutcome(RunOutcome{Snapshot: snapshot, Terminal: &captured}, confine.LocalNetworkGrant{}, nil)
	if !result.OK || result.ExitCode != 0 || snapshot.HasExit || snapshot.Tail != "board\nyour move" {
		t.Fatalf("running capture: result=%+v snapshot=%+v", result, snapshot)
	}
}
