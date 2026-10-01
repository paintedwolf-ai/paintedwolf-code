package approvalstate

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
)

func testLease(action, request, confinement string) hitl.DirectIPLease {
	return hitl.DirectIPLease{
		ActionDigest:         action,
		RequestDigest:        request,
		ConfinementDigest:    confinement,
		DeclaredDestinations: []string{"udp://time.nist.gov:123"},
		CommandSummary:       "ntpdate time.nist.gov",
	}
}

// Each digest participates in the lease identity.
func TestDirectIPLeaseCoversOnlyTheExactAction(t *testing.T) {
	rt := NewDirectIPCapabilityRuntime()
	lease := testLease("action-a", "req-a", "conf-a")
	rt.GrantChat("root", lease, "grant_a", "cp-1", nil)

	if !rt.LeaseCovers("root", lease) {
		t.Fatal("the granted action is not covered by its own lease")
	}
	for name, other := range map[string]hitl.DirectIPLease{
		"different command":     testLease("action-b", "req-a", "conf-a"),
		"widened declaration":   testLease("action-a", "req-b", "conf-a"),
		"different confinement": testLease("action-a", "req-a", "conf-b"),
	} {
		if rt.LeaseCovers("root", other) {
			t.Fatalf("%s rode the lease — the identity triple is the whole predicate", name)
		}
	}
	if rt.LeaseCovers("other-root", lease) {
		t.Fatal("a lease crossed session trees")
	}
}

// Incomplete leases have no authority subject.
func TestDirectIPIncompleteLeaseNeverStoresOrMatches(t *testing.T) {
	rt := NewDirectIPCapabilityRuntime()
	partial := hitl.DirectIPLease{ActionDigest: "action-a", RequestDigest: "req-a"}
	rt.GrantChat("root", partial, "grant_a", "cp-1", nil)
	if rt.LeaseCovers("root", partial) {
		t.Fatal("an incomplete lease matched")
	}
	if len(rt.ListChatGrants("root")) != 0 {
		t.Fatal("an incomplete lease was stored")
	}
	rt.GrantChat("root", testLease("action-a", "req-a", "conf-a"), "grant_b", "cp-2", nil)
	if rt.LeaseCovers("root", partial) {
		t.Fatal("an incomplete probe matched a stored lease")
	}
}

func TestDirectIPLeaseExpiryStopsCoverButKeepsTheRow(t *testing.T) {
	rt := NewDirectIPCapabilityRuntime()
	lease := testLease("action-a", "req-a", "conf-a")
	past := time.Now().UTC().Add(-time.Minute)
	rt.GrantChat("root", lease, "grant_a", "cp-1", &past)

	if rt.LeaseCovers("root", lease) {
		t.Fatal("an expired lease still covered")
	}
	// Expired rows remain available for review.
	if len(rt.ListChatGrants("root")) != 1 {
		t.Fatal("the expired lease left the Settings list")
	}
}

func TestDirectIPLeaseKeepsSeparateInstallers(t *testing.T) {
	rt := NewDirectIPCapabilityRuntime()
	lease := testLease("action-a", "req-a", "conf-a")
	rt.GrantChat("root", lease, "grant_a", "cp-1", nil)
	if !rt.GrantChat("root", lease, "grant_b", "cp-2", nil) {
		t.Fatal("second approval was not recorded")
	}
	if got := len(rt.ListChatGrants("root")); got != 2 {
		t.Fatalf("approval records = %d, want 2", got)
	}
	if _, ok := rt.RevokeByIDInstalledBy("grant_b", "cp-2"); !ok {
		t.Fatal("second installer could not revoke its lease")
	}
	if got := rt.ListChatGrants("root"); len(got) != 1 || got[0].ID != "grant_a" || got[0].SourceCheckpointID != "cp-1" {
		t.Fatalf("first approval changed after rollback: %+v", got)
	}
}

func TestDirectIPLeaseRevokeAndForget(t *testing.T) {
	rt := NewDirectIPCapabilityRuntime()
	lease := testLease("action-a", "req-a", "conf-a")
	rt.GrantChat("root", lease, "grant_a", "cp-1", nil)

	if _, root, ok := rt.FindByID("grant_a"); !ok || root != "root" {
		t.Fatalf("FindByID = %q, %v", root, ok)
	}
	if _, ok := rt.RevokeByID("grant_a"); !ok {
		t.Fatal("revoke reported nothing removed")
	}
	if rt.LeaseCovers("root", lease) {
		t.Fatal("a revoked lease still covered")
	}
	if _, ok := rt.RevokeByID("grant_a"); ok {
		t.Fatal("revoke was not idempotent")
	}

	rt.GrantChat("root", lease, "grant_b", "cp-2", nil)
	rt.ForgetSession("root")
	if rt.LeaseCovers("root", lease) {
		t.Fatal("a lease survived its task")
	}
	if len(rt.ListAllChatGrants()) != 0 {
		t.Fatal("a forgotten task left rows in the unqualified list")
	}
}

func TestApprovedDirectIPLeasesOutliveStop(t *testing.T) {
	rt := NewDirectIPCapabilityRuntime()
	for i := range maxDirectIPRunGrants + 8 {
		lease := testLease("action-"+string(rune('a'+i%26))+string(rune('0'+i/26)), "req", "conf")
		rt.GrantChat("root", lease, "grant_"+lease.ActionDigest, "cp", nil)
	}
	rt.ReleaseRun("root")
	if got := len(rt.ListChatGrants("root")); got != maxDirectIPRunGrants+8 {
		t.Fatalf("lease count after Stop = %d, want every approved lease", got)
	}
}
