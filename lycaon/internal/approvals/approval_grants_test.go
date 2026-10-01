package approvals_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/approvals"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestApprovalGrantsMapsBoundedLeasesAndUnavailableRoot(t *testing.T) {
	existing := t.TempDir()
	missing := filepath.Join(t.TempDir(), "gone")
	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	grants := approvals.ApprovalGrants([]hitl.ApprovalGrant{
		{
			ID: "grant-root", Scope: hitl.ApprovalGrantScopeDevice,
			Predicate: hitl.ApprovalGrantPredicate{Category: string(settings.ApprovalCategoryWriteRoot), Pattern: existing},
			Title:     "Allow on this device for 30 days", Coverage: existing,
			GrantedAt: now, ExpiresAt: &expires, ExpiresWhen: "in 30 days", ReaskWhen: "confinement changes",
		},
		{
			ID: "grant-missing", Scope: hitl.ApprovalGrantScopeProject,
			Predicate: hitl.ApprovalGrantPredicate{Category: string(settings.ApprovalCategoryWriteRoot), Pattern: missing},
			Title:     "Allow for this project for 7 days", Coverage: missing,
			GrantedAt: now, ExpiresAt: &expires, ExpiresWhen: "in 7 days", ReaskWhen: "project changes",
		},
	})
	if len(grants) != 2 {
		t.Fatalf("grants = %+v", grants)
	}
	byID := map[string]wire.ApprovalGrant{}
	for _, grant := range grants {
		byID[grant.ID] = grant
	}
	if byID["grant-root"].Unavailable {
		t.Fatalf("existing root marked unavailable: %+v", byID["grant-root"])
	}
	if !byID["grant-missing"].Unavailable {
		t.Fatalf("missing root should be unavailable: %+v", byID["grant-missing"])
	}
}

func TestApprovalGrantsMapsStructuredFieldsAndSocketUnavailable(t *testing.T) {
	now := time.Now().UTC()
	missingSock := filepath.Join(t.TempDir(), "gone.sock")
	grants := approvals.ApprovalGrants([]hitl.ApprovalGrant{
		{
			ID: "grant-actions", Scope: hitl.ApprovalGrantScopeChat,
			Predicate:      hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategoryActionSet, Pattern: "digest"},
			ChatSessionID:  "chat-a",
			ExactActionSet: []string{"bash:go test ./...", "bash:go vet ./..."},
			GrantedAt:      now,
		},
		{
			ID: "grant-resources", Scope: hitl.ApprovalGrantScopeDevice,
			Predicate: hitl.ApprovalGrantPredicate{Category: string(settings.ApprovalCategoryHostResource), Pattern: "audio.input, screen.capture"},
			GrantedAt: now,
		},
		{
			ID: "grant-sock", Scope: hitl.ApprovalGrantScopeProject,
			Predicate:    hitl.ApprovalGrantPredicate{Category: string(settings.ApprovalCategorySocketPath), Pattern: missingSock},
			ApprovedPath: missingSock, ResolvedPath: missingSock,
			GrantedAt: now,
		},
	})
	if len(grants) != 3 {
		t.Fatalf("grants = %+v", grants)
	}
	byID := map[string]wire.ApprovalGrant{}
	for _, grant := range grants {
		byID[grant.ID] = grant
	}
	actions := byID["grant-actions"]
	if actions.ActionCount != 2 {
		t.Fatalf("action count not on wire: %+v", actions)
	}
	resources := byID["grant-resources"]
	if len(resources.ResourceIDs) != 2 || resources.ResourceIDs[0] != "audio.input" || resources.ResourceIDs[1] != "screen.capture" {
		t.Fatalf("resource ids not split: %+v", resources)
	}
	sock := byID["grant-sock"]
	if !sock.Unavailable {
		t.Fatalf("missing socket should be unavailable: %+v", sock)
	}
	if sock.Repointed {
		t.Fatalf("missing socket must not read as repointed: %+v", sock)
	}
}
