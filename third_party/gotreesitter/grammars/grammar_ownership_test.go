package grammars

import "testing"

func TestTierOneGrammarOwnershipManifest(t *testing.T) {
	if got := len(grammarOwnershipManifest); got != 30 {
		t.Fatalf("Tier 1 grammar count = %d, want 30", got)
	}
	for name, ownership := range grammarOwnershipManifest {
		if ownership.Tier != 1 {
			t.Fatalf("%s tier = %d, want 1", name, ownership.Tier)
		}
	}
}

func TestOwnedGrammarRegistrySource(t *testing.T) {
	for _, name := range []string{"go", "yaml"} {
		t.Run(name, func(t *testing.T) {
			ownership, ok := GrammarOwnershipFor(name)
			if !ok {
				t.Fatalf("missing ownership record for %q", name)
			}
			if ownership.MaintenanceClass != GrammarMaintenanceOwn {
				t.Fatalf("maintenance class = %q, want %q", ownership.MaintenanceClass, GrammarMaintenanceOwn)
			}
			if ownership.UpstreamRepo == "" || ownership.UpstreamCommit == "" || ownership.UpstreamLicense == "" {
				t.Fatalf("owned grammar lacks complete upstream provenance: %+v", ownership)
			}
			entry := DetectLanguageByName(name)
			if entry == nil {
				t.Fatalf("missing registry entry for %q", name)
			}
			if entry.GrammarSource != GrammarSourceGrammargenBlob {
				t.Fatalf("GrammarSource = %q, want %q", entry.GrammarSource, GrammarSourceGrammargenBlob)
			}
		})
	}
}
