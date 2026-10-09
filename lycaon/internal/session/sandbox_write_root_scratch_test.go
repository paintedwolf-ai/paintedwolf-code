package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

// The broker reads the invocation's scratch from the ask, the fact its
// confinement carries: its own scratch is already in bounds, and any other
// session's scratch is control plane, which never raises a card.
func TestWriteRootBrokerReadsScratchFromTheAsk(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	own := enginepaths.SessionScratchUnder(cfg, "chat-own")
	worker := enginepaths.SessionScratchUnder(cfg, "worker-1")
	for _, dir := range []string{own, worker} {
		testutil.FailErr(t, "create scratch "+dir, os.MkdirAll(dir, 0o700))
	}
	broker := &WriteRootCheckpointBroker{
		Checkpoints: unusedWriteRootCheckpoints{t: t},
		Runtime:     approvalstate.NewSandboxPathGrantRuntime(),
	}
	ask := func(proposed, scratch string) command.SandboxWriteRootResult {
		t.Helper()
		got, err := broker.Authorize(t.Context(), command.SandboxWriteRootAsk{
			SessionID: "chat-own", ToolName: "command", ProposedWriteRoot: proposed, SessionScratchRoot: scratch,
		})
		testutil.FailErr(t, "authorize "+proposed, err)
		return got
	}

	if got := ask(filepath.Join(own, "build"), own); !got.Authorized || got.Raised {
		t.Fatalf("own scratch: %+v, want authorized without a card", got)
	}
	if got := ask(filepath.Join(worker, "build"), own); got.Authorized || got.Raised {
		t.Fatalf("another session's scratch: %+v, want a silent refusal", got)
	}
	if got := ask(filepath.Join(own, "build"), ""); got.Authorized || got.Raised {
		t.Fatalf("scratch without the invocation's scratch root: %+v, want a silent refusal", got)
	}
}
