package settings_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestListGrantsUnqualifiedReturnsTaskLeasesAcrossChats(t *testing.T) {
	tmp := t.TempDir()
	stageBundledApprovals(t, nil)
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())

	chatGrant := func(id, chat string) hitl.ApprovalGrant {
		return hitl.ApprovalGrant{
			ID:            id,
			Scope:         hitl.ApprovalGrantScopeChat,
			Predicate:     hitl.ApprovalGrantPredicate{Category: string(settings.ApprovalCategoryTool), Pattern: "read"},
			ChatSessionID: chat,
			Title:         "Allow read for this chat",
			Coverage:      "the read tool",
			GrantedAt:     time.Now().UTC(),
			ExpiresWhen:   "when this task ends",
			ReaskWhen:     "the project changes",
		}
	}
	for _, grant := range []hitl.ApprovalGrant{
		chatGrant("grant_chat_a", "chat-a"),
		chatGrant("grant_chat_b", "chat-b"),
	} {
		created, err := gate.ApplyGrant(grant)
		testutil.FailErr(t, "gate.ApplyGrant failed", err)
		if !created {
			t.Fatalf("grant %s not created", grant.ID)
		}
	}

	all := gate.ListGrants("")
	ids := map[string]bool{}
	for _, grant := range all {
		ids[grant.ID] = true
	}
	if len(all) != 2 || !ids["grant_chat_a"] || !ids["grant_chat_b"] {
		t.Fatalf("unqualified list = %+v want both chats' task leases", all)
	}

	one := gate.ListGrants("chat-a")
	if len(one) != 1 || one[0].ID != "grant_chat_a" {
		t.Fatalf("chat-a list = %+v want only its own lease", one)
	}
}
