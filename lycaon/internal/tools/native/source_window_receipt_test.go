package native

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

type sourceWindowReceipt struct {
	Handle       string                `json:"handle"`
	SourceWindow *hostcmd.SourceWindow `json:"source_window"`
}

func decodeSourceWindowReceipt(t *testing.T, raw string) sourceWindowReceipt {
	t.Helper()
	var receipt sourceWindowReceipt
	testutil.FailErr(t, "decode command result", json.Unmarshal([]byte(raw), &receipt))
	if receipt.SourceWindow == nil || receipt.SourceWindow.ID == "" {
		t.Fatalf("no source window in %s", raw)
	}
	return receipt
}

func TestVerifyResultsStateTheirWindowACCW07(t *testing.T) {
	// Without watcher coverage a finished call settles before its result.
	repochange.DisableWatchersForTest()
	t.Cleanup(repochange.ResetWatchersForTest)

	t.Run("finished", func(t *testing.T) {
		f := newNativeWalkFixture(t)
		tool := &VerifyTool{Runner: hostcmd.NewRunner(), Background: newTestBackgroundRegistry(t)}
		raw, err := tool.Run(t.Context(), map[string]any{"command": "touch verified.txt"}, f.tc)
		testutil.FailErr(t, "run verify", err)
		receipt := decodeSourceWindowReceipt(t, raw)
		if !receipt.SourceWindow.Settled {
			t.Fatalf("finished verify window = %+v", receipt.SourceWindow)
		}
		walk, err := f.ledger.Walk.QueryWalk(t.Context(), "p1", sourceledger.Baseline{Kind: sourceledger.BaselineSession, SessionID: "s1"}, 10, 0, sourceledger.CommitLens{})
		testutil.FailErr(t, "read verify walk", err)
		if len(walk.Files) != 1 || walk.Files[0].Effects[0].CommandWindowID != receipt.SourceWindow.ID {
			t.Fatalf("verify walk = %+v", walk.Files)
		}
	})

	for name, args := range map[string]map[string]any{
		"promoted":   {"command": "sleep 60", "wait_ms": float64(1)},
		"background": {"command": "sleep 60", "background": true},
	} {
		t.Run(name, func(t *testing.T) {
			f := newNativeWalkFixture(t)
			registry := newTestBackgroundRegistry(t)
			tool := &VerifyTool{Runner: hostcmd.NewRunner(), Background: registry}
			raw, err := tool.Run(t.Context(), args, f.tc)
			testutil.FailErr(t, "run verify", err)
			receipt := decodeSourceWindowReceipt(t, raw)
			if receipt.SourceWindow.Settled || receipt.Handle == "" {
				t.Fatalf("running verify result %s", raw)
			}
			listed := f.ledger.Commands.OpenSessionWindows("p1", "s1")
			if len(listed) != 1 || listed[0].ID != receipt.SourceWindow.ID || listed[0].Closing {
				t.Fatalf("open windows = %+v", listed)
			}
			_, err = registry.Lifecycle.Stop("s1", receipt.Handle)
			testutil.FailErr(t, "stop verify", err)
			<-listed[0].Settled
		})
	}
}

func TestCaptureResultStatesItsWindowACCW07(t *testing.T) {
	repochange.DisableWatchersForTest()
	t.Cleanup(repochange.ResetWatchersForTest)
	f := newNativeWalkFixture(t)
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true}, []sandbox.ToolProfile{{ID: f.tc.Identity.Agent, Tools: map[string]bool{"command": true}}})
	tool := &command.CommandTool{Runner: hostcmd.NewRunner(), Boundary: boundary, Background: newTestBackgroundRegistry(t)}
	raw, err := tool.Run(t.Context(), map[string]any{"command": "true", "terminal_capture": map[string]any{}}, f.tc)
	testutil.FailErr(t, "run capture", err)
	if receipt := decodeSourceWindowReceipt(t, raw); !receipt.SourceWindow.Settled {
		t.Fatalf("capture result %s", raw)
	}
}
