package definition

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBundledManifestPhaseGraphIsSimplePath(t *testing.T) {
	raw, _, err := LoadPackManifestsForCatalog(nil)
	testutil.FailErr(t, "loadCatalogManifestsWithSources failed", err)
	for key, m := range raw {
		resolved, err := ResolveManifestChain(m, raw)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		assertPhaseGraph(t, key, resolved)
	}
}

func assertPhaseGraph(t *testing.T, manifestKey string, m Manifest) {
	t.Helper()
	if len(m.Phases) == 0 {
		return
	}
	byID := map[string]PhaseDef{}
	for _, p := range m.PhaseDefs {
		byID[p.ID] = p
	}
	// Every phase in order exists in defs.
	for _, id := range m.Phases {
		if _, ok := byID[id]; !ok {
			t.Fatalf("%s: phase %q missing from PhaseDefs", manifestKey, id)
		}
	}
	// A set next pointer references an existing phase (loops allowed).
	for _, id := range m.Phases {
		def := byID[id]
		if def.Next != "" {
			if _, ok := byID[def.Next]; !ok {
				t.Fatalf("%s: phase %q next=%q is unknown", manifestKey, id, def.Next)
			}
		}
	}
	// No duplicate phase ids.
	seen := map[string]struct{}{}
	for _, id := range m.Phases {
		if _, dup := seen[id]; dup {
			t.Fatalf("%s: duplicate phase %q", manifestKey, id)
		}
		seen[id] = struct{}{}
	}
	// gates_satisfied requires non-empty gates.
	for _, p := range m.PhaseDefs {
		if p.CompleteWhen == CompleteWhenGatesSatisfied && len(p.Gates) == 0 {
			t.Fatalf("%s: phase %q gates_satisfied without gates", manifestKey, p.ID)
		}
	}
}
