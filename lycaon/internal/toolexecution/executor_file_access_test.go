package toolexecution_test

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestAllowOnceWritesExternalFileWithoutReusableAccess(t *testing.T) {
	stageApprovals(t, settings.ApprovalEffectAsk)
	project := t.TempDir()
	// Scratch roots have standing authority; the reviewed file must live elsewhere.
	account, err := user.LookupId(strconv.Itoa(os.Getuid()))
	testutil.FailErr(t, "resolve home for external fixture", err)
	outside, err := os.MkdirTemp(account.HomeDir, ".file-access-test-") //nolint:usetesting // Outside the permitted temporary roots, including the test HOME.
	testutil.FailErr(t, "create external fixture", err)
	t.Cleanup(func() { testutil.FailErr(t, "remove external fixture", os.RemoveAll(outside)) })
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
	testutil.FailErr(t, "create approval store", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	reg := tools.NewDefaultRegistry()
	writer := &native.WriteTool{Boundary: boundary}
	var executed tools.ToolContext
	testutil.FailErr(t, "register native writer", reg.Register("write", func(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
		executed = tc
		return writer.Run(ctx, args, tc)
	}))
	executor := toolexecution.NewExecutor(toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), gate), reg, "implement")
	target := filepath.Join(outside, "new", "nested", "notes.txt")
	tc := tools.ToolContext{
		Roots: []projectroot.RootRef{{ID: "root", Path: project, IsPrimary: true}}, ActiveRootID: "root",
		SessionID: "chat-file-once", ToolCallID: "write-once", Agent: "implement",
	}
	for _, content := range []string{"first approved write", "second approved write"} {
		mgr := &asyncHITL{requested: make(chan struct{}, 1)}
		executor.Approvals.SetCheckpointManager(mgr, gate)
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		done := make(chan error, 1)
		go func() {
			_, invokeErr := executor.Invoke(ctx, "write", map[string]any{"path": target, "content": content}, tc)
			done <- invokeErr
		}()
		select {
		case <-mgr.requested:
			mgr.approve()
		case <-ctx.Done():
			t.Fatal("external write did not ask for its own approval")
		}
		testutil.FailErr(t, "write approved external file", <-done)
		cancel()
		data, readErr := os.ReadFile(target)
		testutil.FailErr(t, "read approved file", readErr)
		if string(data) != content {
			t.Fatalf("written content = %q, want %q", data, content)
		}
		if len(gate.ListGrants(tc.SessionID)) != 0 {
			t.Fatal("allow once installed reusable authority")
		}
		if _, resolveErr := projectpaths.ResolveWrite(t.Context(), nil, executed, filepath.Join(outside, "sibling.txt")); resolveErr == nil {
			t.Fatal("exact approval authorized a sibling")
		}
		fresh := tc
		fresh.ApprovedFileAccess = nil
		fresh.PreparedFileAccess = nil
		fresh.FileChangeReview = nil
		if _, resolveErr := projectpaths.ResolveWrite(t.Context(), nil, fresh, target); resolveErr == nil {
			t.Fatal("approval escaped its invocation context")
		}
		// Context reuse exercises the invocation-boundary approval reset.
		tc = executed
		tc.ToolCallID = "write-again"
		tc.ApprovedFileAccess = []hitl.GrantedPathDelta{{Path: target, Write: true}}
	}
}

// File-change reviews treat the invocation's scratch as session workspace rather than control plane.
func TestWriteToSessionScratchPassesFileChangeReview(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	project := t.TempDir()
	scratch := enginepaths.SessionScratchUnder(cfg, "chat-scratch")
	testutil.FailErr(t, "create session scratch", os.MkdirAll(scratch, 0o700))
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
	testutil.FailErr(t, "create approval store", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	reg := tools.NewDefaultRegistry()
	writer := &native.WriteTool{Boundary: boundary}
	testutil.FailErr(t, "register native writer", reg.Register("write", writer.Run))
	executor := toolexecution.NewExecutor(toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), gate), reg, "implement")
	mgr := &asyncHITL{requested: make(chan struct{}, 1)}
	executor.Approvals.SetCheckpointManager(mgr, gate)
	tc := tools.ToolContext{
		Roots: []projectroot.RootRef{{ID: "root", Path: project, IsPrimary: true}}, ActiveRootID: "root",
		SessionID: "chat-scratch", ToolCallID: "write-scratch", Agent: "implement",
		SessionScratchDir: scratch,
	}
	// An unexpected card would wait for a decision; the deadline turns that into a failure.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, err = executor.Invoke(ctx, "write", map[string]any{"path": "@scratch/flow.sh", "content": "echo ok\n"}, tc)
	testutil.FailErr(t, "write session scratch", err)
	data, err := os.ReadFile(filepath.Join(scratch, "flow.sh"))
	testutil.FailErr(t, "read scratch file", err)
	if string(data) != "echo ok\n" {
		t.Fatalf("scratch content = %q", data)
	}
	select {
	case <-mgr.requested:
		t.Fatal("a write to the session's own scratch asked for approval")
	default:
	}
}

// A control-plane path is refused before the checkpoint bus: the gate denies it
// with the resolver's code, no card is minted, and the handler never runs.
func TestControlPlanePathDeniedBeforeAnyCheckpoint(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	stageApprovals(t, settings.ApprovalEffectAsk)
	project := t.TempDir()
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
	testutil.FailErr(t, "create approval store", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	reg := tools.NewDefaultRegistry()
	invoked := false
	testutil.FailErr(t, "register list_dir", reg.Register("list_dir", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		invoked = true
		return "", nil
	}))
	executor := toolexecution.NewExecutor(toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), gate), reg, "implement")
	mgr := &asyncHITL{requested: make(chan struct{}, 1)}
	executor.Approvals.SetCheckpointManager(mgr, gate)
	tc := tools.ToolContext{
		Roots: []projectroot.RootRef{{ID: "root", Path: project, IsPrimary: true}}, ActiveRootID: "root",
		SessionID: "chat-control-plane", ToolCallID: "list-sessions", Agent: "implement",
	}
	sessions := filepath.Join(cfg, "debug", "sessions")
	_, err = executor.Invoke(t.Context(), "list_dir", map[string]any{"path": sessions}, tc)
	var reject *toolrejection.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != isolation.CodeControlPlaneDenied {
		t.Fatalf("err = %v want %s", err, isolation.CodeControlPlaneDenied)
	}
	if got := reject.Data["path"]; got != sessions {
		t.Fatalf("reject must cite the path, got %v", got)
	}
	select {
	case <-mgr.requested:
		t.Fatal("a control-plane path minted a checkpoint")
	default:
	}
	if invoked {
		t.Fatal("the handler ran for a path the gate denied")
	}
}
