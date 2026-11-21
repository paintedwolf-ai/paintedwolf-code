package capabilityadmin

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
)

type locationGrantFixture struct {
	hitl.ApprovalGate
	grants []hitl.ApprovalGrant
}

func (f *locationGrantFixture) ListGrants(string) []hitl.ApprovalGrant { return f.grants }

func TestLocationAccessFollowsLiveSavedApprovals(t *testing.T) {
	anchor := t.TempDir()
	file := filepath.Join(anchor, "notes.txt")
	before := time.Now().Add(-time.Hour)
	after := time.Now().Add(time.Hour)
	gate := &locationGrantFixture{grants: []hitl.ApprovalGrant{
		{Scope: hitl.ApprovalGrantScopeProject, ProjectID: "foreign",
			GrantedPath: &hitl.GrantedPathDelta{Path: anchor, Write: true, Tree: true}},
		{Scope: hitl.ApprovalGrantScopeChat, ChatSessionID: "other",
			GrantedPath: &hitl.GrantedPathDelta{Path: anchor, Write: true, Tree: true}},
		{Scope: hitl.ApprovalGrantScopeDevice, ExpiresAt: &before,
			GrantedPath: &hitl.GrantedPathDelta{Path: anchor, Write: true, Tree: true}},
		{Scope: hitl.ApprovalGrantScopeProject, ProjectID: "project",
			GrantedPath: &hitl.GrantedPathDelta{Path: anchor, Tree: true}},
		{Scope: hitl.ApprovalGrantScopeChat, ChatSessionID: "current", ExpiresAt: &after,
			GrantedPath: &hitl.GrantedPathDelta{Path: file, Write: true}},
	}}
	inventory := &Inventory{Gate: gate}
	access := inventory.SourceLocationAccess("current", "project", anchor, false)
	if access == nil || access.Mode != "write" || access.Tree || access.Path != file || access.Scope != "chat" || access.ExpiresAt == nil {
		t.Fatalf("saved access = %+v", access)
	}
	gate.grants = gate.grants[:4]
	access = inventory.SourceLocationAccess("current", "project", file, true)
	if access == nil || access.Mode != "read" || !access.Tree || access.Scope != "project" {
		t.Fatalf("remaining saved access = %+v", access)
	}
	gate.grants = gate.grants[:3]
	if access := inventory.SourceLocationAccess("current", "project", anchor, false); access != nil {
		t.Fatalf("unexpected expired or foreign access: %+v", access)
	}
}
