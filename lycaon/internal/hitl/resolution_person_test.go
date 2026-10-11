package hitl_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func commandCheckpoint(sessionID string) hitl.CheckpointRequest {
	return hitl.CheckpointRequest{
		SessionID: sessionID, Kind: api.CheckpointKindToolApproval, Type: hitl.DecisionTypeApprove,
		Title:          "Approve command",
		ProposedAction: &hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
Args: map[string]any{"command": "echo hi"},
},
},
	}
}

func storedResolver(t *testing.T, sqlDB db.Handle, checkpointID string) (resolvedBy string, personID sql.NullString) {
	t.Helper()
	testutil.FailErr(t, "read checkpoint resolution", sqlDB.QueryRowContext(context.Background(),
		`SELECT resolved_by, resolved_by_person_id FROM checkpoints WHERE id = ?`, checkpointID).Scan(&resolvedBy, &personID))
	return resolvedBy, personID
}

func hostOwner(t *testing.T, sqlDB db.Handle) people.Person {
	t.Helper()
	return people.Person{ID: testdbseed.OwnerID(t, sqlDB), Role: api.PersonRoleOwner}
}

func TestAnsweringACheckpointRecordsTheCallerInTheRowAndLedger(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	insertSession(t, sqlDB, sessionID)
	owner := hostOwner(t, sqlDB)
	ctx := people.WithCaller(context.Background(), owner)
	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, commandCheckpoint(sessionID))
	testutil.FailErr(t, "request checkpoint", err)
	approveCurrentOption(t, ctx, mgr, sessionID, resp.CheckpointID)

	resolvedBy, personID := storedResolver(t, sqlDB, resp.CheckpointID)
	if resolvedBy != "human" || personID.String != owner.ID {
		t.Fatalf("checkpoint resolution = %q by %q, want human by %q", resolvedBy, personID.String, owner.ID)
	}
	decisions := eventsByAction(sessionEvents(t, sqlDB, sessionID), authzcontext.EventActionApprovalDecision)
	if len(decisions) != 1 || decisions[0].ResolverPersonID != owner.ID {
		t.Fatalf("approval decisions = %+v, want one resolved by %q", decisions, owner.ID)
	}
}

func TestStoppingRecordsThePersonAndExpiryRecordsNobody(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	insertSession(t, sqlDB, sessionID)
	owner := hostOwner(t, sqlDB)
	ctx := context.Background()

	stopped, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, commandCheckpoint(sessionID))
	testutil.FailErr(t, "request stopped checkpoint", err)
	testutil.FailErr(t, "stop session", mgr.CancelPendingForSession(people.WithCaller(ctx, owner), sessionID, "stopped"))
	if by, person := storedResolver(t, sqlDB, stopped.CheckpointID); by != "user_stop" || person.String != owner.ID {
		t.Fatalf("stop resolution = %q by %q, want user_stop by the stopping person", by, person.String)
	}

	mgr.SetCheckpointExpiry(func() time.Duration { return 10 * time.Millisecond })
	expiring, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, commandCheckpoint(sessionID))
	testutil.FailErr(t, "request expiring checkpoint", err)
	deadline := time.Now().Add(2 * time.Second)
	for {
		final, err := mgr.PollCheckpoint(ctx, expiring.CheckpointID)
		testutil.FailErr(t, "poll expiring checkpoint", err)
		if final.Status == hitl.DecisionStatusExpired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("checkpoint never expired")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if by, person := storedResolver(t, sqlDB, expiring.CheckpointID); by != "expiry" || person.Valid {
		t.Fatalf("expiry resolution = %q by %q, want expiry by nobody", by, person.String)
	}
	for _, event := range eventsByAction(sessionEvents(t, sqlDB, sessionID), authzcontext.EventActionApprovalDecision) {
		wantPerson := owner.ID
		if event.ResolvedBy == authzcontext.ResolvedByExpiry {
			wantPerson = ""
		}
		if event.ResolverPersonID != wantPerson {
			t.Fatalf("%s decision resolver = %q, want %q", event.ResolvedBy, event.ResolverPersonID, wantPerson)
		}
	}
}

func TestStopNoPersonRequestedIsTheHosts(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	insertSession(t, sqlDB, sessionID)
	ctx := context.Background()

	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, commandCheckpoint(sessionID))
	testutil.FailErr(t, "request checkpoint", err)
	testutil.FailErr(t, "stop session without a caller", mgr.CancelPendingForSession(ctx, sessionID, ""))
	if by, person := storedResolver(t, sqlDB, resp.CheckpointID); by != "host_stop" || person.Valid {
		t.Fatalf("stop resolution = %q by %q, want host_stop by nobody", by, person.String)
	}
	decisions := eventsByAction(sessionEvents(t, sqlDB, sessionID), authzcontext.EventActionApprovalDecision)
	if len(decisions) != 1 || decisions[0].ResolvedBy != authzcontext.ResolvedByHostStop || decisions[0].ResolverPersonID != "" {
		t.Fatalf("host stop decision = %+v, want host_stop with no person", decisions)
	}
}

func TestAnsweringWithoutACallerIsRefusedBeforeTheStore(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	insertSession(t, sqlDB, sessionID)
	ctx := context.Background()

	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, commandCheckpoint(sessionID))
	testutil.FailErr(t, "request checkpoint", err)
	mgr.Authority.SetApprovalAuthorityInstaller(noopApprovalInstaller{})
	_, err = mgr.Authority.ResolveApprovalOption(ctx, sessionID, resp.CheckpointID, "approve_current_action")
	if !errors.Is(err, people.ErrNoDecidingPerson) {
		t.Fatalf("answer without a caller err = %v, want %v", err, people.ErrNoDecidingPerson)
	}
	var status string
	testutil.FailErr(t, "read checkpoint", sqlDB.QueryRowContext(ctx,
		`SELECT status FROM checkpoints WHERE id = ?`, resp.CheckpointID).Scan(&status))
	if status != "pending" {
		t.Fatalf("refused answer left the checkpoint %s", status)
	}
}

func TestPolicyResolutionRecordsNobodyInTheRowAndLedger(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	insertSession(t, sqlDB, sessionID)
	ctx := context.Background()

	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, commandCheckpoint(sessionID))
	testutil.FailErr(t, "request checkpoint", err)

	mgr.Authority.SetApprovalAuthorityInstaller(noopApprovalInstaller{})
	policy := authzledger.PolicyIdentity{PackID: "acme-policy", UnitID: "approvals/ci", RuleID: "allow-echo"}
	_, err = mgr.Authority.ResolveApprovalOptionBy(ctx, sessionID, resp.CheckpointID, "approve_current_action", hitl.PolicyApproval(policy))
	testutil.FailErr(t, "resolve checkpoint by policy", err)

	resolvedBy, personID := storedResolver(t, sqlDB, resp.CheckpointID)
	if resolvedBy != "policy" || personID.Valid {
		t.Fatalf("policy resolution = %q by %q, want policy by nobody", resolvedBy, personID.String)
	}

	decisions := eventsByAction(sessionEvents(t, sqlDB, sessionID), authzcontext.EventActionApprovalDecision)
	if len(decisions) != 1 || decisions[0].ResolvedBy != authzcontext.ResolvedByPolicy || decisions[0].ResolverPersonID != "" {
		t.Fatalf("policy decision = %+v, want resolved_by policy with empty person ID", decisions)
	}
	var detail authzcontext.EventDetail
	testutil.FailErr(t, "decode policy decision detail", json.Unmarshal([]byte(decisions[0].DetailJSON), &detail))
	if detail.ResolverPolicy == nil || *detail.ResolverPolicy != (authzcontext.ResolverPolicy{PackID: "acme-policy", UnitID: "approvals/ci", RuleID: "allow-echo"}) {
		t.Fatalf("policy decision detail = %+v, want the resolving rule", detail.ResolverPolicy)
	}
}

func TestPolicyResolutionWithoutIdentityIsRejectedBeforeTheStore(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	insertSession(t, sqlDB, sessionID)
	ctx := context.Background()

	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, commandCheckpoint(sessionID))
	testutil.FailErr(t, "request checkpoint", err)
	mgr.Authority.SetApprovalAuthorityInstaller(noopApprovalInstaller{})
	for _, policy := range []authzledger.PolicyIdentity{{}, {PackID: "acme-policy", UnitID: "approvals/ci"}} {
		_, err = mgr.Authority.ResolveApprovalOptionBy(ctx, sessionID, resp.CheckpointID, "approve_current_action", hitl.PolicyApproval(policy))
		if !errors.Is(err, hitl.ErrApprovalResolverInvalid) {
			t.Fatalf("incomplete policy %+v resolved: %v", policy, err)
		}
	}
	var status string
	var resolvedBy sql.NullString
	testutil.FailErr(t, "read checkpoint", sqlDB.QueryRowContext(ctx,
		`SELECT status, resolved_by FROM checkpoints WHERE id = ?`, resp.CheckpointID).Scan(&status, &resolvedBy))
	if status != "pending" || resolvedBy.Valid {
		t.Fatalf("rejected resolver touched the checkpoint: status=%s resolved_by=%v", status, resolvedBy)
	}
	if decisions := eventsByAction(sessionEvents(t, sqlDB, sessionID), authzcontext.EventActionApprovalDecision); len(decisions) != 0 {
		t.Fatalf("rejected resolver sealed ledger decisions: %+v", decisions)
	}
}
