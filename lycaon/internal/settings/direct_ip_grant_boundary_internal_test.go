package settings

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/pkg/api"
)

func directIPGrant() hitl.ApprovalGrant {
	return hitl.ApprovalGrant{
		ID:            "grant_directip",
		Scope:         hitl.ApprovalGrantScopeChat,
		Predicate:     hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategoryDirectIP, Pattern: "a\x00b\x00c"},
		ChatSessionID: "sess-ladder",
		ProjectDir:    "/tmp/proj",
		Title:         "Allow for this chat",
		GrantedAt:     time.Now().UTC(),
	}
}

// Direct-IP grants are declined by the durable approvals store.
func TestDirectIPGrantNeverReachesTheDurableStore(t *testing.T) {
	approvals := ladderGate(t)
	created, err := approvals.ApplyGrant(directIPGrant())
	if err != nil {
		t.Fatalf("ApplyGrant returned an error rather than declining: %v", err)
	}
	if created {
		t.Fatal("a direct-IP lease was created by the generic grant path")
	}
	if got := len(approvals.store.GlobalGrants()); got != 0 {
		t.Fatalf("durable grants = %d, want 0", got)
	}
	if got := len(approvals.grants.live("sess-ladder")); got != 0 {
		t.Fatalf("session grants = %d, want 0 — the runtime holds this lease", got)
	}
}

// Direct-IP leases are checked by runtime identity, outside generic rule matching.
func TestDirectIPGrantNeverMatchesGenerically(t *testing.T) {
	action := hitl.ProposedAction{
		Tool:       "command",
		Args:       map[string]any{"command": "ntpdate time.nist.gov"},
		ProjectDir: "/tmp/proj",
		SessionID:  "sess-ladder",
	}
	if grantMatchesAction(directIPGrant(), action) {
		t.Fatal("a direct-IP lease matched through generic action matching")
	}
}

// Direct-IP leases are not persisted as policy rules or durable grants.
func TestDirectIPCategoryIsNotDurableOrPolicy(t *testing.T) {
	if allowedPolicyCategories[ApprovalCategory(hitl.ApprovalGrantCategoryDirectIP)] {
		t.Fatal("direct_ip became an authorable policy rule category")
	}
	if allowedDurableGrantCategories[ApprovalCategory(hitl.ApprovalGrantCategoryDirectIP)] {
		t.Fatal("direct_ip became a durable grant category")
	}
	err := validateApprovalConfig(ApprovalConfig{Grants: []ApprovalGrant{{
		ID: "grant_x", Scope: hitl.ApprovalGrantScopeProject, ProjectDir: "/tmp/proj",
		Category: ApprovalCategory(hitl.ApprovalGrantCategoryDirectIP), Pattern: "a\x00b\x00c", Title: "x",
	}}})
	if err == nil {
		t.Fatal("a persisted direct_ip grant validated")
	}
}

// The absorbing capability card owns the time rung and recommended offer.
func TestAbsorbedGrantOffersCarryNoTimeRung(t *testing.T) {
	approvals := ladderGate(t)
	action := ladderAction()
	action.HostResources = []string{"camera"}
	result := &hitl.ApprovalResult{Decision: askDecision(api.GateUserRule), HostResourceApproval: true}

	offered := approvals.GrantOffers(action, result)
	absorbed := approvals.AbsorbedGrantOffers(action, result)
	if len(absorbed) == 0 {
		t.Fatal("the absorbed ladder is empty — the rungs below would pass vacuously")
	}
	if len(absorbed) >= len(offered) {
		t.Fatalf("absorbed offers = %d, offered = %d — the absorbed ladder must drop the time rung", len(absorbed), len(offered))
	}
	for _, offer := range absorbed {
		if offer.TTLSeconds > 0 {
			t.Fatalf("absorbed ladder minted a second time rung: %+v", offer)
		}
		if offer.Group == "" && offer.Rung == hitl.ApprovalRungChat {
			t.Fatalf("absorbed ladder must not mint a primary task rung: %+v", offer)
		}
	}
}
