package hitl_test

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
)

// TestContainedProvenanceMatchesDefaultConfinement asserts the Contained value the
// gate stamps equals the confinement exec.Run would apply for the same roots.
func TestContainedProvenanceMatchesDefaultConfinement(t *testing.T) {
	confine.TestingSetAutoConfine(t)
	roots := []string{t.TempDir()}

	c, ok := confine.DefaultConfinement(confine.Request{Roots: roots})
	got := hitl.ContainedForRequest(confine.Request{Roots: roots})
	fromSame := hitl.ContainedFromConfinement(c, ok)

	if !reflect.DeepEqual(got, fromSame) {
		t.Fatalf("ContainedForRequest diverged from ContainedFromConfinement: got=%+v fromSame=%+v", got, fromSame)
	}
	if ok {
		if !got.FSJailed {
			t.Fatal("Contained.FSJailed=false when DefaultConfinement returned ok")
		}
		if got.Egress != hitl.ContainedEgressLabel(c.Network) {
			t.Fatalf("Contained.Egress=%q want %q (network=%v)", got.Egress, hitl.ContainedEgressLabel(c.Network), c.Network)
		}
		if !reflect.DeepEqual(got.Roots, c.Roots) {
			t.Fatalf("Contained.Roots=%v want %v", got.Roots, c.Roots)
		}
	} else if got.FSJailed {
		t.Fatalf("Contained.FSJailed=true when DefaultConfinement returned !ok: %+v", got)
	}

	empty := hitl.ContainedForRequest(confine.Request{})
	if empty.FSJailed || empty.Egress != "" || len(empty.Roots) != 0 {
		t.Fatalf("empty roots must yield zero Contained, got %+v", empty)
	}
}

// TestContainedCarriesDirectIPNarrowing asserts the card and the executed box describe the
// same authority. The gate stamps Contained from its own DefaultConfinement call, so
// narrowing must reach this projection as well as the profile.
func TestContainedCarriesDirectIPNarrowing(t *testing.T) {
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_SANDBOX_NETWORK", "")
	roots := []string{t.TempDir()}

	narrowed := confine.Request{
		Roots: roots, Egress: confine.EgressDirectIP,
		DirectIPDeclared: []string{"udp://time.nist.gov:123"},
	}
	c, ok := confine.DefaultConfinement(narrowed)
	gate := hitl.ContainedForRequest(narrowed)
	if !reflect.DeepEqual(gate, hitl.ContainedFromConfinement(c, ok)) {
		t.Fatal("gate stamp must equal the projection of the confinement the executor applies")
	}
	if !ok {
		t.Skip("confinement unavailable on this platform")
	}
	if !gate.DirectIP {
		t.Fatalf("narrowing must reach the approval fact: %+v", gate)
	}

	wide := confine.Request{Roots: roots, Egress: confine.EgressDirectIP}
	wideStamp := hitl.ContainedForRequest(wide)

	// Approving UDP/123 once must not license the open direct-IP box.
	narrow := gate.EffectiveBoundaryPermits()
	if len(narrow) == 0 || reflect.DeepEqual(narrow, wideStamp.EffectiveBoundaryPermits()) {
		t.Fatalf("narrowed and wide direct IP must be different authorities: %+v", narrow)
	}

	other := confine.Request{
		Roots: roots, Egress: confine.EgressDirectIP,
		DirectIPDeclared: []string{"tcp://db.internal:5432"},
	}
	if reflect.DeepEqual(hitl.ContainedForRequest(other).EffectiveBoundaryPermits(), narrow) {
		t.Fatal("udp/123 and tcp/5432 must be different authorities")
	}

	// A declaration on a mediated request must not influence anything: narrowing exists
	// only for the mode that would otherwise be wide open.
	mediated := hitl.ContainedForRequest(confine.Request{
		Roots: roots, DirectIPDeclared: []string{"udp://time.nist.gov:123"},
	})
	if mediated.DirectIP {
		t.Fatalf("declarations must be inert outside direct IP: %+v", mediated)
	}
}

func TestContainedWriteRootsCarryDurableGrants(t *testing.T) {
	confine.TestingSetAutoConfine(t)
	project := t.TempDir()
	granted := t.TempDir()
	confine.SetGrantedWriteRootsSource(func(string) []string { return []string{granted} })
	t.Cleanup(func() { confine.SetGrantedWriteRootsSource(nil) })

	got := hitl.ContainedForRequest(confine.Request{Roots: []string{project}})
	if !got.FSJailed {
		t.Skip("confinement unavailable on this platform")
	}
	if !confine.PathWithinWriteRoots(granted, got.WriteRoots) {
		t.Fatalf("granted root missing from WriteRoots: %v", got.WriteRoots)
	}
	if !confine.PathWithinWriteRoots(project, got.WriteRoots) {
		t.Fatalf("project root missing from WriteRoots: %v", got.WriteRoots)
	}
	for _, root := range got.Roots {
		if root == granted {
			t.Fatal("granted root leaked into Roots")
		}
	}
}

func TestContainedTaskOverlayStaysOutOfRoots(t *testing.T) {
	confine.TestingSetAutoConfine(t)
	project := t.TempDir()
	overlay := filepath.Join(t.TempDir(), "not-yet-created")
	got := hitl.ContainedForAction(hitl.ActionConfineInputs{
		Roots:             []string{project},
		OverlayWriteRoots: []string{overlay},
	})
	if !got.FSJailed {
		t.Skip("confinement unavailable on this platform")
	}
	if !confine.PathWithinWriteRoots(overlay, got.WriteRoots) {
		t.Fatalf("task overlay missing from WriteRoots: %v", got.WriteRoots)
	}
	for _, root := range got.Roots {
		if root == overlay {
			t.Fatal("task overlay leaked into Roots")
		}
	}
	plain := hitl.ContainedForAction(hitl.ActionConfineInputs{Roots: []string{project}})
	if !plain.FSJailed {
		t.Skip("confinement unavailable on this platform")
	}
	if hitl.GrantKey(hitl.ProposedAction{Tool: "command", Contained: got}) !=
		hitl.GrantKey(hitl.ProposedAction{Tool: "command", Contained: plain}) {
		t.Fatal("GrantKey moved after a write-root overlay")
	}
}
