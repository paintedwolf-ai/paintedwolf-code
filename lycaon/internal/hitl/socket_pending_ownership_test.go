//go:build linux || darwin

package hitl

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPendingSocketOwnersCloseAtActualCheckpointTerminalBoundaries(t *testing.T) {
	for _, terminal := range []string{"cancel", "expire", "deny", "shutdown"} {
		t.Run(terminal, func(t *testing.T) {
			root, err := os.MkdirTemp("/tmp", "pc-") //nolint:usetesting // Native retained aliases have bounded addresses.
			testutil.FailErr(t, "pending socket fixture", err)
			defer os.RemoveAll(root)
			t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(root, "state"))
			path := filepath.Join(root, "service")
			listener, err := net.Listen("unix", path)
			testutil.FailErr(t, "pending socket listener", err)
			defer listener.Close()
			capture, err := socketref.Capture(path)
			testutil.FailErr(t, "pending socket capture", err)
			defer capture.Close()
			resolved, err := filepath.EvalSymlinks(path)
			testutil.FailErr(t, "pending socket label", err)
			grant := confine.SocketGrant{ApprovedPath: path, ResolvedPath: resolved, Binding: capture.Binding()}
			database := testdbfixture.Open(t, "pending-owner.db")
			testdbseed.InsertSessionWithRoot(t, database, "chat", testdbseed.DefaultProjectID, root)
			ctx := testdbseed.OwnerCaller(t, t.Context(), database)
			manager := NewCheckpoints(NewSQLStore(database), &events.Publisher{Hub: events.NewMemoryHub()}, &fakeAuthzRecorder{})
			defer func() { testutil.FailErr(t, "stop pending owner", manager.StopExpiryTimers(context.Background())) }()
			action := ProposedAction{Invocation: ActionInvocation{Tool: "command", Args: map[string]any{"command": "fixture"}}, Scope: ActionScope{ProjectID: testdbseed.DefaultProjectID, ProjectDir: root, SessionID: "chat"}, Sockets: ActionSockets{SocketGrants: []confine.SocketGrant{grant}}}
			_, decision := gate.Evaluate(gate.Facts{Stage: gate.StagePreSpawn, Ran: gate.ProducerApprovalRequest, ApprovalRequest: &gate.ApprovalRequest{Count: 1}}, gate.DefaultPosture)
			primary, cited, reasons := PresentDecision(decision)
			option := ApprovalOption{ID: "once", Kind: ApprovalOptionCurrentAction, Rung: ApprovalRungOnce, Title: TitleAllowOnce, Coverage: "this service for this action", ExpiresWhen: ExpiresAfterThisAction, ReaskWhen: "the next action", DecisionAction: ApprovalOptionApprove, Authority: []ApprovalAuthorityDelta{{Kind: AuthoritySocketPermit, ChatSessionID: "chat", SessionID: "chat", ToolCallID: "call", ActionDigest: GrantKey(action), Sockets: []ApprovalSocketTarget{{ApprovedPath: path, ResolvedPath: resolved, Binding: grant.Binding}}}}}
			plan, err := NewApprovalPlan(action, ApprovalStagePreSpawn, ApprovalSubject{Kind: ApprovalSubjectSocketSet, Title: "Allow local service", Targets: []ApprovalTarget{{Kind: "socket", Label: path}}}, ApprovalPresentation{Action: "Connect to local service", Impact: "Use the reviewed service", Gate: primary, Cited: cited}, reasons, []ApprovalOption{option}, FaceContext{})
			testutil.FailErr(t, "pending socket plan", err)
			pending, err := manager.RequestCheckpoint(ctx, CheckpointRequest{SessionID: "chat", ProjectID: testdbseed.DefaultProjectID, ToolCallID: "call", Kind: api.CheckpointKindToolApproval, ProposedAction: &action, ApprovalPlan: plan, Decision: decision})
			testutil.FailErr(t, "persist pending socket approval", err)
			capture.Close()
			borrowed, err := grant.Binding.Acquire()
			testutil.FailErr(t, "pending checkpoint owns reference", err)
			borrowed.Close()
			want := DecisionStatusCanceled
			switch terminal {
			case "cancel":
				err = manager.CancelPendingForSession(ctx, "chat", "fixture cancellation")
			case "expire":
				err = manager.expirePending(ctx, pending.CheckpointID, expiryReason)
				want = DecisionStatusExpired
			case "deny":
				_, err = manager.ResolveCheckpoint(ctx, "chat", pending.CheckpointID, api.CheckpointKindToolApproval, &DecisionResult{Approved: false}, nil)
				want = DecisionStatusRejected
			case "shutdown":
				err = manager.StopExpiryTimers(context.Background())
				want = DecisionStatusPending
			}
			testutil.FailErr(t, "settle pending socket owner", err)
			row, err := manager.PollCheckpoint(ctx, pending.CheckpointID)
			testutil.FailErr(t, "pending history survives", err)
			if row.Status != want {
				t.Fatalf("terminal history=%s, want %s", row.Status, want)
			}
			borrowed, err = grant.Binding.Acquire()
			if borrowed != nil {
				borrowed.Close()
			}
			if !errors.Is(err, socketref.ErrUnbound) {
				t.Fatalf("terminal checkpoint retained socket without GC: %v", err)
			}
		})
	}
}
