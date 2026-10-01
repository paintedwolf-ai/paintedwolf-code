package tools

import (
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
)

func directIPLadderAction() hitl.ProposedAction {
	return hitl.ProposedAction{
		Tool:              "command",
		Args:              map[string]any{"command": "ntpdate time.nist.gov"},
		Command:           "ntpdate time.nist.gov",
		SessionID:         "sess-direct",
		ProjectDir:        "/tmp/proj",
		DirectIPRequested: true,
		Contained: hitl.Contained{
			FSJailed: true,
			Egress:   hitl.ContainedEgressDirectIP,
			Roots:    []string{"/tmp/proj"},
			DirectIP: true,
		},
	}
}

func directIPTestLease() hitl.DirectIPLease {
	return hitl.DirectIPLease{
		ActionDigest:         "action-a",
		RequestDigest:        "req-a",
		ConfinementDigest:    "conf-a",
		DeclaredDestinations: []string{"udp://time.nist.gov:123"},
		CommandSummary:       "ntpdate time.nist.gov",
	}
}

// The direct-IP ladder is two chat-scoped rungs plus a disabled project slot: the
// host cannot describe this authority by destination, so nothing outlives the chat.
func TestDirectIPLadderIsDayAndTaskOnly(t *testing.T) {
	offers := DirectIPExecutionGrantOffers(directIPLadderAction(), directIPTestLease())
	if len(offers) != 3 {
		t.Fatalf("offers = %d want day, task, and the disabled durable slot: %+v", len(offers), offers)
	}
	if slot := offers[2]; !slot.Disabled || slot.Rung != hitl.ApprovalRungProject || slot.Note != hitl.NoteEndsWithChat {
		t.Fatalf("durable slot = %+v, want a disabled project row", slot)
	}
	offers = offers[:2]
	ttlCount, recommendedCount := 0, 0
	for i, offer := range offers {
		if offer.Scope != hitl.ApprovalGrantScopeChat {
			t.Fatalf("offer %d scope = %s — direct IP never leaves chat scope", i, offer.Scope)
		}
		if offer.Grant.Predicate.Category != hitl.ApprovalGrantCategoryDirectIP {
			t.Fatalf("offer %d category = %q", i, offer.Grant.Predicate.Category)
		}
		if offer.TTLSeconds > 0 {
			ttlCount++
			if i != 0 {
				t.Fatalf("time rung at position %d — the ladder puts it first", i)
			}
			if offer.Title != hitl.TitleAllowFor1Day {
				t.Fatalf("time rung title = %q", offer.Title)
			}
		}
		if offer.Rung == hitl.ApprovalRungChat {
			recommendedCount++
		}
	}
	if ttlCount != 1 || recommendedCount != 1 {
		t.Fatalf("time rungs = %d, recommended = %d, want exactly 1 of each", ttlCount, recommendedCount)
	}
	if offers[0].ID == offers[1].ID {
		t.Fatal("the hour and task rungs share an id")
	}
}

// Direct-IP chat leases are keyed to confinement roots and egress mode, so
// commands with different declarations share one lease while differing roots do not.
func TestDirectIPOfferIdentityFollowsTheAction(t *testing.T) {
	action := directIPLadderAction()
	base := DirectIPExecutionGrantOffers(action, directIPTestLease())
	again := DirectIPExecutionGrantOffers(action, directIPTestLease())
	if base[1].ID != again[1].ID {
		t.Fatal("the same action minted two lease identities")
	}
	widened := directIPTestLease()
	widened.RequestDigest = "req-WIDER"
	if DirectIPExecutionGrantOffers(action, widened)[1].ID != base[1].ID {
		t.Fatal("commands with different declarations under the same roots should share the task lease")
	}
	differingRoots := directIPLadderAction()
	differingRoots.Contained.Roots = []string{"/other/path"}
	if DirectIPExecutionGrantOffers(differingRoots, directIPTestLease())[1].ID == base[1].ID {
		t.Fatal("differing confinement roots should mint different lease identities")
	}
	expectedPattern := hitl.DirectIPChatConfinementDigest(action.Contained.Roots, action.Contained.Egress)
	if base[1].Grant.Predicate.Pattern != expectedPattern {
		t.Fatalf("predicate pattern = %q, want %q", base[1].Grant.Predicate.Pattern, expectedPattern)
	}
}

func TestDirectIPIncompleteLeaseMintsNoOffers(t *testing.T) {
	partial := hitl.DirectIPLease{ActionDigest: "action-a", CommandSummary: "curl example.com"}
	if offers := DirectIPExecutionGrantOffers(directIPLadderAction(), partial); offers != nil {
		t.Fatalf("an incomplete lease minted offers: %+v", offers)
	}
}

// Ladder and lease stand-down follow Decision.Reuse — not a parallel withhold list.
func TestDirectIPLadderFollowsDecisionReuse(t *testing.T) {
	for _, g := range gate.All() {
		result := &hitl.ApprovalResult{Decision: &gate.Decision{
			Primary: g, Cited: []gate.Fact{{Key: "test", Value: "fixture", Source: "test"}},
		}}
		mayOffer := result.Decision == nil || result.Decision.Reuse().Offered()
		if mayOffer != gate.ReuseFor(g).Offered() {
			t.Fatalf("%s: mayOffer=%v but Gate.Reuse.Offered=%v", g, mayOffer, gate.ReuseFor(g).Offered())
		}
	}
	silent := &hitl.ApprovalResult{}
	if !(silent.Decision == nil || silent.Decision.Reuse().Offered()) {
		t.Fatal("a silent result must not withhold reuse")
	}
}

func TestDirectIPWitnessPinsJailNotTransportNarrowing(t *testing.T) {
	wide := directIPLadderAction()
	wide.Contained.BoundaryPermits = []hitl.BoundaryPermit{
		{Kind: hitl.BoundaryPermitDirectIP, Digest: "enabled", Count: 1},
	}
	narrowed := directIPLadderAction()
	narrowed.Contained.BoundaryPermits = []hitl.BoundaryPermit{
		{Kind: hitl.BoundaryPermitDirectIP, Digest: "udp-123-digest", Count: 1},
	}

	if !hitl.WitnessEqual(hitl.BoundaryWitness(wide.Contained), hitl.BoundaryWitness(narrowed.Contained)) {
		t.Fatal("transport narrowing changed the jail witness")
	}
	if directIPConfinementDigest(wide.Contained) == directIPConfinementDigest(narrowed.Contained) {
		t.Fatal("UDP/123 and the open box must not share a confinement digest")
	}
}
