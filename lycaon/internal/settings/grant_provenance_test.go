package settings_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDurableGrantsNameWhoApprovedThem(t *testing.T) {
	global, _, _ := testSettingsPaths(t)
	store, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	policy := &authzledger.PolicyIdentity{PackID: "acme-policy", UnitID: "approvals/ci", RuleID: "allow-host"}

	cases := []struct {
		name    string
		host    string
		person  string
		policy  *authzledger.PolicyIdentity
		persist bool
	}{
		{name: "nobody", host: "nobody.example.test"},
		{name: "a person", host: "person.example.test", person: testutil.HostOwner().ID, persist: true},
		{name: "a policy", host: "policy.example.test", policy: policy, persist: true},
		{name: "both", host: "both.example.test", person: testutil.HostOwner().ID, policy: policy},
		{name: "an incomplete policy", host: "partial.example.test", policy: &authzledger.PolicyIdentity{PackID: "acme-policy"}},
	}
	for _, tc := range cases {
		grant := durableGrant(settings.ApprovalCategoryHost, tc.host, hitl.ApprovalGrantScopeDevice, "")
		grant.GrantedByPersonID, grant.GrantedByPolicy = tc.person, tc.policy
		_, err := store.UpsertGlobalGrant(grant)
		if tc.persist {
			testutil.FailErr(t, "persist grant approved by "+tc.name, err)
			continue
		}
		if err == nil {
			t.Fatalf("grant approved by %s was persisted", tc.name)
		}
	}

	stored := map[string]settings.ApprovalGrant{}
	for _, grant := range store.GlobalGrants() {
		stored[grant.Pattern] = grant
	}
	if got := stored["person.example.test"].GrantedByPersonID; got != testutil.HostOwner().ID {
		t.Fatalf("person grant granted_by_person_id = %q", got)
	}
	if got := stored["policy.example.test"].GrantedByPolicy; got == nil || *got != *policy {
		t.Fatalf("policy grant granted_by_policy = %+v", got)
	}

	reloaded, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "reload approvals", err)
	for _, grant := range reloaded.GlobalGrants() {
		if !grant.ToDomain().Granted() {
			t.Fatalf("reloaded grant %s lost who approved it", grant.ID)
		}
	}
}
