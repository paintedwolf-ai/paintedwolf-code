package spawn

import (
	"slices"
	"testing"
)

// The ambient roster is the only surface that dispatches a pack agent outside
// a workflow naming it.
func TestAmbientRosterIncludesContributedAgents(t *testing.T) {
	t.Cleanup(func() { SetContributedAgentsSource(nil) })
	SetContributedAgentsSource(func() []string { return []string{"acme-reader"} })

	roster := AmbientAllowedAgents()
	if !slices.Contains(roster, "acme-reader") {
		t.Fatalf("roster %v is missing the contributed agent", roster)
	}
	for _, bundled := range bundledSpawnConfig().AllowedAgents {
		if !slices.Contains(roster, bundled) {
			t.Fatalf("roster %v dropped bundled agent %q", roster, bundled)
		}
	}
}

// A contributed id colliding with a bundled one names the same agent, and the
// roster feeds the task() enum, so it is a set.
func TestAmbientRosterDedupesAndIsStable(t *testing.T) {
	t.Cleanup(func() { SetContributedAgentsSource(nil) })
	bundled := bundledSpawnConfig().AllowedAgents
	SetContributedAgentsSource(func() []string { return []string{bundled[0], "b-agent", "a-agent"} })

	first := AmbientAllowedAgents()
	if got := count(first, bundled[0]); got != 1 {
		t.Fatalf("bundled agent %q appears %d times in %v", bundled[0], got, first)
	}
	if !slices.Equal(first, AmbientAllowedAgents()) {
		t.Fatal("roster must be stable across calls")
	}
	if idx := slices.Index(first, "a-agent"); idx == -1 || first[idx+1] != "b-agent" {
		t.Fatalf("contributed agents should be sorted after the bundled ones: %v", first)
	}
}

// With no source installed the roster is exactly the bundled list.
func TestAmbientRosterWithoutASourceIsBundled(t *testing.T) {
	t.Cleanup(func() { SetContributedAgentsSource(nil) })
	SetContributedAgentsSource(nil)
	if !slices.Equal(AmbientAllowedAgents(), bundledSpawnConfig().AllowedAgents) {
		t.Fatal("without a source the roster is the bundled list")
	}
}

func count(list []string, want string) int {
	n := 0
	for _, v := range list {
		if v == want {
			n++
		}
	}
	return n
}
