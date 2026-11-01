package command_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

func TestProgramFailureLoopBlocksFourthIdenticalFailureAndResetsOnSuccess(t *testing.T) {
	f := newProgramFixture(t)
	f.Runtime.FailureTracker = command.NewCommandFailureTracker()
	const failing = "external -- /bin/echo hit | tee side.txt; false"
	for i := range 3 {
		out, err := f.call(t, t.Context(), failing)
		testutil.FailErr(t, "run failing program", err)
		if outcome := decodeOutcome(t, out); outcome.State != "completed" || outcome.Status == 0 {
			t.Fatalf("attempt %d outcome: %+v", i, outcome)
		}
	}
	testutil.FailErr(t, "remove side effect before blocked retry", os.Remove(filepath.Join(f.Dir, "side.txt")))
	_, err := f.call(t, t.Context(), failing)
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != "COMMAND_FAILURE_LOOP" || reject.Data["command"] != failing || reject.Data["count"] != 3 {
		t.Fatalf("fourth attempt reject = %+v (err=%v)", reject, err)
	}
	if _, statErr := os.Stat(filepath.Join(f.Dir, "side.txt")); !os.IsNotExist(statErr) {
		t.Fatal("blocked fourth attempt executed side effect")
	}
	if programs := f.programRows(t); len(programs) != 3 {
		t.Fatalf("blocked fourth attempt recorded program row: %v", programs)
	}

	for range 2 {
		out, err := f.call(t, t.Context(), "false")
		testutil.FailErr(t, "run false", err)
		if outcome := decodeOutcome(t, out); outcome.Status == 0 {
			t.Fatalf("expected non-zero status: %+v", outcome)
		}
	}
	out, err := f.call(t, t.Context(), "true")
	testutil.FailErr(t, "run true to reset streak", err)
	if outcome := decodeOutcome(t, out); outcome.Status != 0 {
		t.Fatalf("expected zero status: %+v", outcome)
	}
	for i := range 3 {
		out, err := f.call(t, t.Context(), "false")
		testutil.FailErr(t, "run false after reset", err)
		if outcome := decodeOutcome(t, out); outcome.Status == 0 {
			t.Fatalf("post-reset attempt %d outcome: %+v", i, outcome)
		}
	}
	_, err = f.call(t, t.Context(), "false")
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "COMMAND_FAILURE_LOOP" {
		t.Fatalf("expected failure loop after three post-reset failures, got %v", err)
	}
}

func TestProgramFailureLoopChangedApproachStartsFreshStreak(t *testing.T) {
	f := newProgramFixture(t)
	f.Runtime.FailureTracker = command.NewCommandFailureTracker()
	testutil.FailErr(t, "create subdir", os.MkdirAll(filepath.Join(f.Dir, "sub"), 0o755))

	for range 3 {
		_, err := f.CallArgs(t, t.Context(), map[string]any{"command": "false"})
		testutil.FailErr(t, "run baseline failing command", err)
	}
	if _, err := f.CallArgs(t, t.Context(), map[string]any{"command": "false"}); toolrejection.AsToolReject(err) == nil {
		t.Fatal("expected baseline command to be blocked on 4th attempt")
	}

	if _, err := f.CallArgs(t, t.Context(), map[string]any{"command": "test -f missing.txt"}); err != nil {
		t.Fatalf("changed command should start a fresh approach: %v", err)
	}

	for range 3 {
		_, err := f.CallArgs(t, t.Context(), map[string]any{"command": "false"})
		testutil.FailErr(t, "re-saturate baseline failing command", err)
	}
	for range 3 {
		_, err := f.CallArgs(t, t.Context(), map[string]any{"command": "false", "cwd": "sub"})
		testutil.FailErr(t, "changed cwd should start a fresh approach", err)
	}
	if _, err := f.CallArgs(t, t.Context(), map[string]any{"command": "false", "cwd": "sub"}); toolrejection.AsToolReject(err) == nil {
		t.Fatal("expected sub cwd approach to block on 4th attempt")
	}

	if _, err := f.CallArgs(t, t.Context(), map[string]any{
		"command":            "false",
		"cwd":                "sub",
		"capability_request": map[string]any{"direct_ip": map[string]any{}},
	}); err != nil {
		t.Fatalf("changed capability_request should start a fresh approach: %v", err)
	}
}

func TestProgramFailureLoopAdmitsObservedWorkspaceSourceChange(t *testing.T) {
	f := newProgramFixture(t)
	f.Runtime.FailureTracker = command.NewCommandFailureTracker()
	ledger := sourceledger.New(testdbfixture.Open(t, "source.db"), filepath.Join(t.TempDir(), "content"))
	t.Cleanup(func() { testutil.FailErr(t, "close snapshots", ledger.Snapshots.Close()) })
	f.Configure = func(tc *tools.ToolContext) {
		tc.Source.Observations = ledger.Inventory
	}

	for range 3 {
		out, err := f.call(t, t.Context(), "test -f ready.txt")
		testutil.FailErr(t, "run failing check", err)
		if outcome := decodeOutcome(t, out); outcome.Status == 0 {
			t.Fatalf("expected missing file failure: %+v", outcome)
		}
	}
	repochange.Advance(f.Dir)
	_, err := f.call(t, t.Context(), "test -f ready.txt")
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != "COMMAND_FAILURE_LOOP" || reject.Data["failure_source_unchanged"] != true {
		t.Fatalf("watcher noise without content change bypassed loop: %+v (err=%v)", reject, err)
	}

	testutil.FailErr(t, "write ready.txt", os.WriteFile(filepath.Join(f.Dir, "ready.txt"), []byte("ok\n"), 0o600))
	repochange.Advance(f.Dir)
	out, err := f.call(t, t.Context(), "test -f ready.txt")
	testutil.FailErr(t, "retry after admitted source change", err)
	if outcome := decodeOutcome(t, out); outcome.Status != 0 {
		t.Fatalf("expected zero status after source repair: %+v", outcome)
	}
}

func TestProgramFailureLoopInterruptedProgramDoesNotAdvanceStreak(t *testing.T) {
	f := newProgramFixture(t)
	f.Runtime.FailureTracker = command.NewCommandFailureTracker()
	const prog = "test -f hold.txt && external -- /bin/sleep 30; false"

	for range 2 {
		out, err := f.call(t, t.Context(), prog)
		testutil.FailErr(t, "run fast failing program", err)
		if outcome := decodeOutcome(t, out); outcome.Status == 0 {
			t.Fatalf("expected failure status: %+v", outcome)
		}
	}

	testutil.FailErr(t, "write hold.txt", os.WriteFile(filepath.Join(f.Dir, "hold.txt"), []byte("1"), 0o600))
	done := make(chan error, 1)
	go func() {
		_, err := f.call(t, t.Context(), prog)
		done <- err
	}()
	f.awaitExecution(t, "command", "running")
	if stopped := f.Runtime.Supervisor.Stop(f.Session); len(stopped) != 1 {
		t.Fatalf("expected one stopped program, got %v", stopped)
	}
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("interrupted program did not settle")
	}
	testutil.FailErr(t, "remove hold.txt", os.Remove(filepath.Join(f.Dir, "hold.txt")))

	out, err := f.call(t, t.Context(), prog)
	testutil.FailErr(t, "third completed failure after interruption should run", err)
	if outcome := decodeOutcome(t, out); outcome.Status == 0 {
		t.Fatalf("expected non-zero status on third completed run: %+v", outcome)
	}
	_, err = f.call(t, t.Context(), prog)
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "COMMAND_FAILURE_LOOP" {
		t.Fatalf("expected fourth completed attempt to block, got %v", err)
	}
}

func TestCommandToolPropagatesFailureTrackerToProgramRuntime(t *testing.T) {
	f := newProgramFixture(t)
	var tc tools.ToolContext
	f.Configure = func(current *tools.ToolContext) { tc = *current }
	_, err := f.call(t, t.Context(), "true")
	testutil.FailErr(t, "seed template context", err)
	tool := &command.CommandTool{Runner: hostcmd.NewRunner(), Boundary: f.Boundary, Programs: f.Runtime}
	contract, _ := toolcontract.Lookup("command")
	runTool := func(i int) error {
		callID := fmt.Sprintf("tool-call-%d", i)
		parent, err := f.Recorder.Begin(t.Context(), invocation.Start{ProjectID: testdbseed.DefaultProjectID, SessionID: f.Session, ToolCallID: callID, ToolName: "command", Args: map[string]any{"command": "false"}, Contract: contract})
		testutil.FailErr(t, "begin parent receipt", err)
		callTC := tc
		callTC.Identity.ToolCallID = callID
		callTC.Invocation.ID = parent.ID
		_, err = tool.Run(t.Context(), map[string]any{"command": "false"}, callTC)
		return err
	}

	for i := range 3 {
		testutil.FailErr(t, "run failing command through CommandTool", runTool(i))
	}
	err = runTool(3)
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "COMMAND_FAILURE_LOOP" {
		t.Fatalf("expected CommandTool program path to enforce COMMAND_FAILURE_LOOP, got %v", err)
	}
}
