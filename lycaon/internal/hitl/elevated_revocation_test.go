package hitl_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type heldElevatedInstaller struct{ entered, release chan struct{} }

func (h heldElevatedInstaller) InstallApprovalOption(ctx context.Context, _ string, _ hitl.ApprovalOption) (func(), error) {
	close(h.entered)
	select {
	case <-h.release:
		return func() {}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestRevocationWaitsForInstallAndSeal(t *testing.T) {
	database, manager, sessionID := newTestManager(t)
	timed, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ctx := testdbseed.OwnerCaller(t, timed, database)
	insertSession(t, database, sessionID)
	action := hitl.ProposedAction{Tool: "command", SessionID: sessionID, Args: map[string]any{"command": "make"}}
	grant := hitl.ApprovalGrant{ID: "grant_elevated", Scope: hitl.ApprovalGrantScopeChat, ChatSessionID: sessionID, Title: hitl.TitleAllowForThisChat,
		ElevatedEffects: []api.ElevatedAccessEffect{api.ElevatedAccessEffectHostExecution}}
	presentation, reasons := approvalPlanPresentation()
	plan, err := hitl.NewApprovalPlan(action, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{
		Kind: hitl.ApprovalSubjectAction, Title: "Run make", Targets: []hitl.ApprovalTarget{{Kind: "action", Label: "make"}},
	}, presentation, reasons, []hitl.ApprovalOption{{
		ID: "chat", Kind: hitl.ApprovalOptionLease, Scope: hitl.ApprovalGrantScopeChat, Rung: hitl.ApprovalRungChat,
		Title: hitl.TitleAllowForThisChat, Coverage: "make", ExpiresWhen: hitl.ExpiresWhenChatDeleted, ReaskWhen: "the command changes",
		DecisionAction: hitl.ApprovalOptionApprove, Authority: []hitl.ApprovalAuthorityDelta{{Kind: hitl.AuthorityGenericGrant, Grant: &grant}},
	}}, hitl.FaceContext{})
	testutil.FailErr(t, "create plan", err)
	checkpoint, err := manager.RequestCheckpoint(ctx, hitl.CheckpointRequest{SessionID: sessionID, Kind: api.CheckpointKindToolApproval, ProposedAction: &action, ApprovalPlan: plan})
	testutil.FailErr(t, "create checkpoint", err)
	held := heldElevatedInstaller{entered: make(chan struct{}), release: make(chan struct{})}
	manager.Authority.SetApprovalAuthorityInstaller(held)
	resolved := make(chan error, 1)
	go func() {
		_, err := manager.Authority.ResolveApprovalOption(ctx, sessionID, checkpoint.CheckpointID, "chat")
		resolved <- err
	}()
	select {
	case <-held.entered:
	case <-ctx.Done():
		t.Fatal("installation did not begin")
	}
	revoked := make(chan error, 1)
	go func() {
		release := manager.Authority.LockApprovalAuthority()
		defer release()
		found, err := manager.Authority.ForgetChatGrant(ctx, grant.ID)
		if err == nil && !found {
			err = fmt.Errorf("revocation missed the installed grant's durable record")
		}
		revoked <- err
	}()
	select {
	case err := <-revoked:
		t.Fatalf("revocation crossed an unsealed installation: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(held.release)
	testutil.FailErr(t, "resolve approval", <-resolved)
	testutil.FailErr(t, "revoke committed authority", <-revoked)
	restarted := &recordingInstaller{}
	manager.Authority.SetApprovalAuthorityInstaller(restarted)
	testutil.FailErr(t, "restore after revoke", manager.Authority.RestoreChatGrants(ctx))
	if len(restarted.grantIDs()) != 0 {
		t.Fatal("revoked authority returned after restart")
	}
}
