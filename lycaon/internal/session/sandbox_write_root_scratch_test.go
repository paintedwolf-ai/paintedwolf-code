package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/settings"
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

func TestWriteRootAndReadPathBrokerResults(t *testing.T) {
	runtime := approvalstate.NewSandboxPathGrantRuntime()
	broker := &WriteRootCheckpointBroker{
		Runtime:           runtime,
		ReadRuntime:       runtime,
		Checkpoints:       unusedWriteRootCheckpoints{t: t},
		ApprovalsDisabled: func(string) bool { return true },
	}
	ctx := t.Context()
	projectDir := t.TempDir()

	// 1. InRoots ordinary subject (sandbox_write_root_broker.go line 99)
	resInRoots, err := broker.Authorize(ctx, command.SandboxWriteRootAsk{
		SessionID: "sess", ProjectDir: projectDir, ProposedWriteRoot: filepath.Join(projectDir, "sub"),
	})
	testutil.FailErr(t, "Authorize in roots", err)
	if !resInRoots.Authorized {
		t.Fatalf("resInRoots = %+v, want authorized", resInRoots)
	}

	// 2. ApprovalsDisabled autoGrant (sandbox_write_root_broker.go line 154)
	outsideRoot := filepath.Join(t.TempDir(), "outside")
	resAuto, err := broker.Authorize(ctx, command.SandboxWriteRootAsk{
		SessionID: "sess", ProjectDir: projectDir, ProposedWriteRoot: outsideRoot,
	})
	testutil.FailErr(t, "Authorize approvals disabled", err)
	if !resAuto.Authorized {
		t.Fatalf("resAuto = %+v, want authorized", resAuto)
	}

	// 3. Exact overlay match for non-ordinary subject (sandbox_write_root_broker.go line 102)
	exactPath := filepath.Join(t.TempDir(), ".ssh", "id_ed25519")
	runtime.GrantSessionWriteRoot("sess", exactPath)
	resExact, err := broker.Authorize(ctx, command.SandboxWriteRootAsk{
		SessionID: "sess", ProjectDir: projectDir, ProposedWriteRoot: exactPath,
	})
	testutil.FailErr(t, "Authorize exact non-ordinary", err)
	if !resExact.Authorized {
		t.Fatalf("resExact = %+v, want authorized", resExact)
	}

	// 4. Rule deny (sandbox_write_root_broker.go line 110)
	broker.ApprovalsDisabled = nil
	broker.Rule = func(context.Context, string, string, string) (settings.ApprovalRule, bool) {
		return settings.ApprovalRule{Effect: settings.ApprovalEffectDeny}, true
	}
	resDeny, err := broker.Authorize(ctx, command.SandboxWriteRootAsk{
		SessionID: "sess", ProjectDir: projectDir, ProposedWriteRoot: filepath.Join(t.TempDir(), "denied"),
	})
	testutil.FailErr(t, "Authorize rule deny", err)
	if !resDeny.Denied {
		t.Fatalf("resDeny = %+v, want denied", resDeny)
	}

	// 5. AuthorizeRead autoGrant (sandbox_read_path_broker.go line 69)
	broker.Rule = nil
	broker.ApprovalsDisabled = func(string) bool { return true }
	readRes, err := broker.AuthorizeRead(ctx, command.SandboxReadPathAsk{
		SessionID: "sess", ProjectDir: projectDir, ProposedReadPath: filepath.Join(t.TempDir(), "read_any"),
	})
	testutil.FailErr(t, "AuthorizeRead", err)
	if !readRes.Authorized {
		t.Fatalf("readRes = %+v, want authorized", readRes)
	}
}
