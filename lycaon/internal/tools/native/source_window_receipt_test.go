package native

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sandbox"
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
