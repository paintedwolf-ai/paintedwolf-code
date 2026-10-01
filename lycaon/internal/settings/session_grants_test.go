package settings

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
)

func TestSessionGrantPreservesInstallerOnDuplicateInstall(t *testing.T) {
	store := newSessionGrants()
	grant := hitl.ApprovalGrant{
		ID: "grant-task", Scope: hitl.ApprovalGrantScopeChat, ChatSessionID: "session",
		OwnerOperationID: "checkpoint-old",
	}
	if !store.put(grant) {
		t.Fatal("first task grant was not installed")
	}
	grant.OwnerOperationID = "checkpoint-new"
	if store.put(grant) {
		t.Fatal("duplicate active task grant reported newly installed")
	}
	if store.revokeInstalledBy(grant.ID, "checkpoint-new") {
		t.Fatal("later operation replaced the installer of an existing task grant")
	}
	if !store.revokeInstalledBy(grant.ID, "checkpoint-old") {
		t.Fatal("original operation no longer identifies its task grant")
	}
}

func TestSessionGrantReissuesExpiredAuthority(t *testing.T) {
	store := newSessionGrants()
	expired := time.Now().Add(-time.Minute)
	grant := hitl.ApprovalGrant{
		ID: "grant-task", Scope: hitl.ApprovalGrantScopeChat, ChatSessionID: "session",
		ExpiresAt: &expired, OwnerOperationID: "checkpoint-old",
	}
	if !store.put(grant) {
		t.Fatal("expired fixture was not installed")
	}
	grant.ExpiresAt = nil
	grant.OwnerOperationID = "checkpoint-new"
	if !store.put(grant) {
		t.Fatal("expired task grant was not reissued")
	}
	if !store.revokeInstalledBy(grant.ID, "checkpoint-new") {
		t.Fatal("reissued task grant has the wrong installer")
	}
}

func TestExactGrantCannotMatchUnencodableAction(t *testing.T) {
	action := hitl.ProposedAction{Tool: "command", Args: map[string]any{"invalid": make(chan int)}}
	grant := hitl.ApprovalGrant{ExactActionSet: []string{"", hitl.GrantKey(action)}}
	if grantMatchesAction(grant, action) {
		t.Fatal("unencodable action matched an empty exact-action identity")
	}
}
