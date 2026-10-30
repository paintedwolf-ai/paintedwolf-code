//go:build linux || darwin

package capabilityadmin

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/capabilitygrants"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/httpaction"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestUnixSocketPendingRestartWithdrawsStaleCardAndFreshApprovalExecutes(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "sr-") //nolint:usetesting // Socket addresses have a kernel length bound.
	testutil.FailErr(t, "restart fixture", err)
	defer os.RemoveAll(root)
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(root, "state"))
	workspace := filepath.Join(root, "workspace")
	testutil.FailErr(t, "restart workspace", os.Mkdir(workspace, 0700))
	path := filepath.Join(root, "service")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	testutil.FailErr(t, "restart listener", err)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	database := testdbfixture.Open(t, "restart.db")
	testdbseed.InsertSessionWithRoot(t, database, "root-1", testdbseed.DefaultProjectID, workspace)
	ctx := testdbseed.OwnerCaller(t, t.Context(), database)
	store := hitl.NewSQLStore(database)
	newManager := func() *hitl.Checkpoints {
		return hitl.NewCheckpoints(store, &events.Publisher{Hub: events.NewMemoryHub()}, authzcontext.SQLRecorder(database))
	}
	capture := func() (confine.SocketGrant, *socketref.Lease) {
		owner, err := socketref.Capture(path)
		testutil.FailErr(t, "restart capture", err)
		resolved, err := filepath.EvalSymlinks(path)
		testutil.FailErr(t, "restart resolved label", err)
		return confine.SocketGrant{ApprovedPath: path, ResolvedPath: resolved, Binding: owner.Binding()}, owner
	}
	_, decision := gate.Evaluate(gate.Facts{Stage: gate.StagePreSpawn, Ran: gate.ProducerApprovalRequest, ApprovalRequest: &gate.ApprovalRequest{Count: 1}}, gate.DefaultPosture)
	request := func(grant confine.SocketGrant) (hitl.CheckpointRequest, hitl.ProposedAction) {
		action := hitl.ProposedAction{Invocation: hitl.ActionInvocation{Tool: "http_request", Args: map[string]any{"url": "http://service.invalid/", "unix_socket": path}}, Scope: hitl.ActionScope{ProjectID: testdbseed.DefaultProjectID, ProjectDir: workspace, SessionID: "root-1", RootSessionID: "root-1"}, Sockets: hitl.ActionSockets{SocketGrants: []confine.SocketGrant{grant}}}
		return hitl.CheckpointRequest{Decision: decision, SessionID: "root-1", ProjectID: testdbseed.DefaultProjectID, ToolCallID: "call", Kind: wire.CheckpointKindToolApproval, ProposedAction: &action, CoalesceChat: "root-1", CoalesceGrantKey: "same-service", SocketCapability: &hitl.SocketCapability{Targets: []hitl.SocketCapabilityTarget{{ApprovedPath: grant.ApprovedPath, ResolvedPath: grant.ResolvedPath, Binding: grant.Binding}}, EffectiveAuthority: hitl.SocketAuthorityOutsideSandboxDaemon}, GrantOffers: capabilitygrants.SocketExecutionGrantOffers(action, []confine.SocketGrant{grant})}, action
	}
	oldGrant, oldOwner := capture()
	old := newManager()
	oldRequest, _ := request(oldGrant)
	pending, err := old.RequestCheckpoint(ctx, oldRequest)
	testutil.FailErr(t, "persist pending old approval", err)
	oldOwner.Close()
	borrowed, err := oldGrant.Binding.Acquire()
	testutil.FailErr(t, "pending journal retains live reference", err)
	borrowed.Close()
	testutil.FailErr(t, "stop pending reference owner", old.StopExpiryTimers(context.Background()))
	if lease, err := oldGrant.Binding.Acquire(); !errors.Is(err, socketref.ErrUnbound) {
		if lease != nil {
			lease.Close()
		}
		t.Fatalf("engine shutdown left old binding active: %v", err)
	}
	restarted := newManager()
	defer func() {
		testutil.FailErr(t, "stop restarted manager", restarted.StopExpiryTimers(context.Background()))
	}()
	restored := 0
	restarted.Authority.SetToolApprovalRestoreHook(func(hitl.StoredCheckpoint) { restored++ })
	testutil.FailErr(t, "restore pending journal", restarted.RestorePending(ctx))
	stale, err := restarted.PollCheckpoint(ctx, pending.CheckpointID)
	testutil.FailErr(t, "read terminal stale card", err)
	if stale.Status != hitl.DecisionStatusCanceled || restored != 0 {
		t.Fatalf("stale pending/coalescing survived: status=%s, restored=%d", stale.Status, restored)
	}
	if _, err := restarted.Authority.ResolveApprovalOption(ctx, "root-1", pending.CheckpointID, "approve_socket_set_once"); !errors.Is(err, hitl.ErrCheckpointNotPending) {
		t.Fatalf("old approval after restart = %v", err)
	}
	approvals, err := settings.NewApprovalStoreAt(filepath.Join(root, "approvals.yaml"))
	testutil.FailErr(t, "restart approval store", err)
	defer approvals.Close()
	gate := settings.NewRuleApprovalGate(approvals, settings.NoSources())
	runtime := approvalstate.NewSocketCapabilityRuntime()
	defer runtime.Close()
	installer := &Installation{Gate: gate, Sockets: runtime, authorityMu: &sync.Mutex{}}
	restarted.Authority.SetApprovalAuthorityInstaller(installer)
	freshGrant, freshOwner := capture()
	defer freshOwner.Close()
	freshRequest, action := request(freshGrant)
	fresh, err := restarted.RequestCheckpoint(ctx, freshRequest)
	testutil.FailErr(t, "request fresh approval", err)
	if fresh.CheckpointID == pending.CheckpointID {
		t.Fatal("fresh request joined impossible old approval")
	}
	_, err = restarted.Authority.ResolveApprovalOption(ctx, "root-1", fresh.CheckpointID, "approve_socket_set_once")
	testutil.FailErr(t, "approve fresh socket object", err)
	freshOwner.Close()
	registry := tools.NewDefaultRegistry()
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: "implement", Tools: map[string]bool{"http_request": true}}})
	testutil.FailErr(t, "register restarted native owner", httpaction.Register(registry, httpaction.Deps{Boundary: boundary}))
	operation, scope := socketref.WithScope(ctx)
	tc := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "root-1", ProjectID: testdbseed.DefaultProjectID, ToolCallID: "call", Agent: "implement"}, Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: workspace, IsPrimary: true}}, ActiveRootID: "root"}, Socket: tools.InvocationSocket{SocketGrants: []confine.SocketGrant{freshGrant}, SocketActionDigest: hitl.GrantKey(action), SocketCapabilityRuntime: runtime}}
	_, err = registry.Run(operation, "http_request", action.Invocation.Args, tc)
	scope.Close()
	testutil.FailErr(t, "execute fresh approved native request", err)
	if lease, err := freshGrant.Binding.Acquire(); !errors.Is(err, socketref.ErrUnbound) {
		if lease != nil {
			lease.Close()
		}
		t.Fatalf("completed one-use authority survived scope teardown: %v", err)
	}
	again := newManager()
	defer func() { testutil.FailErr(t, "stop second restart", again.StopExpiryTimers(context.Background())) }()
	testutil.FailErr(t, "restore again", again.RestorePending(ctx))
	stale, err = again.PollCheckpoint(ctx, pending.CheckpointID)
	testutil.FailErr(t, "retained terminal history", err)
	if stale.Status != hitl.DecisionStatusCanceled {
		t.Fatal("second restart changed old terminal history")
	}
}
