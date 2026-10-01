package settings

import (
	"testing"

	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/pkg/api"
)

// tunnelEgressAction names the opaque destination's host, port, and transport.
func tunnelEgressAction(host string, port uint16, transport egressproxy.Transport) hitl.ProposedAction {
	return hitl.ProposedAction{
		Tool: "network",
		Args: map[string]any{
			"host": host, "transport": string(transport), "port": port,
		},
		SessionID:  "sess-tunnel",
		ProjectID:  "proj-tunnel",
		ProjectDir: "/tmp/proj",
		Contained: hitl.Contained{
			FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{"/tmp/proj"},
		},
	}
}

// requestEgressAction is the ask for a readable request, which names no port.
func requestEgressAction(host string) hitl.ProposedAction {
	return hitl.ProposedAction{
		Tool: "network",
		Args: map[string]any{
			"host": host, "transport": string(egressproxy.TransportHTTPRequest),
		},
		SessionID:  "sess-tunnel",
		ProjectID:  "proj-tunnel",
		ProjectDir: "/tmp/proj",
		Contained: hitl.Contained{
			FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{"/tmp/proj"},
		},
	}
}

// Tunnel site leases retain the reviewed port.
func TestTunnelAskLeasesTheSiteBoundToItsPort(t *testing.T) {
	for _, transport := range []egressproxy.Transport{
		egressproxy.TransportSocksTCP, egressproxy.TransportHTTPConnect,
	} {
		predicate := GrantPredicateForAction(tunnelEgressAction("git.example.com", 22, transport))
		if predicate.Category != ApprovalCategoryHost || predicate.Pattern != "*.example.com:22" {
			t.Fatalf("%s tunnel predicate = %+v, want the site bound to port 22", transport, predicate)
		}
	}
	readable := GrantPredicateForAction(requestEgressAction("docs.example.com"))
	if readable.Category != ApprovalCategoryHost || readable.Pattern != "*.example.com" {
		t.Fatalf("readable request predicate = %+v, want the registrable site", readable)
	}
}

// A family lease bound to a port covers sibling hosts on that port and nothing
// on another port or transport class.
func TestTunnelFamilyLeaseCoversSiblingsOnThePortOnly(t *testing.T) {
	grant := hitl.ApprovalGrant{
		Scope:     hitl.ApprovalGrantScopeProject,
		Predicate: hitl.ApprovalGrantPredicate{Category: string(ApprovalCategoryHost), Pattern: "*.example.com:443"},
		ProjectID: "proj-tunnel",
	}
	for _, covered := range []hitl.ProposedAction{
		tunnelEgressAction("api.example.com", 443, egressproxy.TransportHTTPConnect),
		tunnelEgressAction("uploads.example.com", 443, egressproxy.TransportSocksTCP),
	} {
		if !grantMatchesAction(grant, covered) {
			t.Fatalf("family lease did not cover %v", covered.Args)
		}
	}
	for _, other := range []hitl.ProposedAction{
		tunnelEgressAction("api.example.com", 22, egressproxy.TransportHTTPConnect),
		tunnelEgressAction("api.other.com", 443, egressproxy.TransportHTTPConnect),
		requestEgressAction("docs.example.com"),
	} {
		if grantMatchesAction(grant, other) {
			t.Fatalf("family lease on :443 covered %v", other.Args)
		}
	}
}

// Exact tunnel grants bind host, port, and transport.
func TestTunnelReuseCarriesPortAndTransport(t *testing.T) {
	approved := tunnelEgressAction("git.example.com", 22, egressproxy.TransportSocksTCP)
	grant := hitl.ApprovalGrant{
		Scope:          hitl.ApprovalGrantScopeChat,
		Predicate:      hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategoryActionSet},
		ProjectID:      approved.ProjectID,
		ChatSessionID:  approved.ChatSession(),
		ExactActionSet: []string{hitl.GrantKey(approved)},
	}
	if !grantMatchesAction(grant, approved) {
		t.Fatal("the approved tunnel was not covered by its own lease")
	}
	for _, other := range []hitl.ProposedAction{
		tunnelEgressAction("git.example.com", 443, egressproxy.TransportSocksTCP),
		tunnelEgressAction("git.example.com", 22, egressproxy.TransportHTTPConnect),
		tunnelEgressAction("exfil.example.com", 22, egressproxy.TransportSocksTCP),
	} {
		if grantMatchesAction(grant, other) {
			t.Fatalf("tunnel lease covered %v", other.Args)
		}
	}
}

// Readable-request grants do not cover opaque tunnels.
func TestHostLeaseDoesNotCoverAnOpaqueTunnel(t *testing.T) {
	grant := hitl.ApprovalGrant{
		Scope:     hitl.ApprovalGrantScopeProject,
		Predicate: hitl.ApprovalGrantPredicate{Category: string(ApprovalCategoryHost), Pattern: "*.example.com"},
		ProjectID: "proj-tunnel",
	}
	if !grantMatchesAction(grant, requestEgressAction("docs.example.com")) {
		t.Fatal("host lease must still cover a readable request to the leased site")
	}
	for _, tunnel := range []hitl.ProposedAction{
		tunnelEgressAction("git.example.com", 22, egressproxy.TransportSocksTCP),
		tunnelEgressAction("exfil.example.com", 443, egressproxy.TransportHTTPConnect),
	} {
		if grantMatchesAction(grant, tunnel) {
			t.Fatalf("host lease covered opaque tunnel %v", tunnel.Args)
		}
	}
}

// The ladder a tunnel card offers: day and task lease the exact host and port,
// and the durable slot leases the site family on that port.
func TestTunnelCardLaddersExactThenFamily(t *testing.T) {
	approvals := ladderGate(t)
	action := tunnelEgressAction("git.example.com", 22, egressproxy.TransportSocksTCP)
	result := &hitl.ApprovalResult{Decision: askDecision(api.GateAgentChosenOutbound)}
	offers := approvals.GrantOffers(action, result)
	if len(offers) != 3 {
		t.Fatalf("tunnel ladder = %d offers, want day, task, project: %+v", len(offers), offers)
	}
	for _, offer := range offers[:2] {
		if offer.Subject != gate.ReuseExactAction {
			t.Fatalf("tunnel offer %q has subject %q, want exact-action authority", offer.ID, offer.Subject)
		}
		if len(offer.Grant.ExactActionSet) != 1 || offer.Grant.ExactActionSet[0] != hitl.GrantKey(action) {
			t.Fatalf("tunnel offer %q does not enumerate the reviewed call", offer.ID)
		}
	}
	family := offers[2]
	if family.Rung != hitl.ApprovalRungProject || family.Disabled || family.Grant.Predicate.Pattern != "*.example.com:22" {
		t.Fatalf("durable slot = %+v, want the site family on port 22", family)
	}
	if family.ReaskWhen != hitl.ReaskWhenDifferentSiteOrPort {
		t.Fatalf("family reask = %q", family.ReaskWhen)
	}
}

func TestHostIPv6LeasePreservesAddressAndPort(t *testing.T) {
	t.Parallel()
	readable := requestEgressAction("2001:db8::443")
	tunnel := tunnelEgressAction("2001:db8::443", 443, egressproxy.TransportHTTPConnect)
	for _, tc := range []struct {
		action     hitl.ProposedAction
		pattern    string
		otherClass hitl.ProposedAction
	}{
		{readable, "2001:db8::443", tunnel},
		{tunnel, "[2001:db8::443]:443", readable},
	} {
		predicate := GrantPredicateForAction(tc.action)
		if predicate.Pattern != tc.pattern {
			t.Fatalf("IPv6 predicate = %q, want %q", predicate.Pattern, tc.pattern)
		}
		grant := hitl.ApprovalGrant{
			Scope:     hitl.ApprovalGrantScopeProject,
			ProjectID: tc.action.ProjectID,
			Predicate: hitl.ApprovalGrantPredicate{Category: string(predicate.Category), Pattern: predicate.Pattern},
		}
		if !grantMatchesAction(grant, tc.action) {
			t.Fatalf("IPv6 grant did not cover its source action: %v", tc.action.Args)
		}
		for _, other := range []hitl.ProposedAction{
			tc.otherClass,
			requestEgressAction("2001:db8::444"),
			tunnelEgressAction("2001:db8::444", 443, egressproxy.TransportHTTPConnect),
			tunnelEgressAction("2001:db8::443", 22, egressproxy.TransportHTTPConnect),
		} {
			if grantMatchesAction(grant, other) {
				t.Fatalf("IPv6 grant %q covered %v", predicate.Pattern, other.Args)
			}
		}
	}
}
