package extpacks

import "testing"

// TestLockForGraphDoesNotAliasCurrent keeps candidate and current storage independent.
func TestLockForGraphDoesNotAliasCurrent(t *testing.T) {
	current := LockFile{Packages: []LockedPackage{
		{
			ID:        "foxden/nightfox",
			Version:   "1.1.0",
			Source:    "https://example.com/pw-pack-nightfox.git",
			Revision:  "rev1",
			Integrity: "sha256:aaa",
			Kind:      PackKindGit,
		},
	}}
	desired := DesiredState{Packs: []DesiredPack{{ID: "foxden/nightfox"}}}
	graph := map[string]packageCandidate{
		"foxden/nightfox": {
			Manifest:  Manifest{ID: "foxden/nightfox", Version: "1.2.0"},
			Source:    "https://example.com/pw-pack-nightfox.git",
			Revision:  "rev2",
			Integrity: "sha256:bbb",
			Kind:      PackKindGit,
		},
	}

	candidate := lockForGraph(current, desired, graph)

	got, ok := current.Package("foxden/nightfox")
	if !ok {
		t.Fatalf("current lock lost its package entirely")
	}
	if got.Version != "1.1.0" || got.Revision != "rev1" || got.Integrity != "sha256:aaa" {
		t.Fatalf("lockForGraph mutated current: got %+v, want version 1.1.0/rev1/sha256:aaa", got)
	}

	changes := LockChanges(current, candidate)
	if len(changes) != 1 {
		t.Fatalf("got %d changes, want 1: %+v", len(changes), changes)
	}
	change := changes[0]
	if change.Kind != "upgraded" {
		t.Fatalf("change.Kind = %q, want %q (this is the exact live symptom: a real bump misreported as unchanged)", change.Kind, "upgraded")
	}
	if change.CurrentVersion != "1.1.0" || change.CandidateVersion != "1.2.0" {
		t.Fatalf("change = %+v, want CurrentVersion 1.1.0 and CandidateVersion 1.2.0", change)
	}
}

func TestLockChangesDetectsUpgradeDowngradeAndRevision(t *testing.T) {
	current := LockFile{Packages: []LockedPackage{
		{ID: "a", Version: "1.0.0", Revision: "r1", Integrity: "sha256:1"},
		{ID: "b", Version: "2.0.0", Revision: "r1", Integrity: "sha256:1"},
		{ID: "c", Version: "1.0.0", Revision: "r1", Integrity: "sha256:1"},
	}}
	candidate := LockFile{Packages: []LockedPackage{
		{ID: "a", Version: "1.1.0", Revision: "r2", Integrity: "sha256:2"},
		{ID: "b", Version: "1.0.0", Revision: "r1", Integrity: "sha256:1"},
		{ID: "c", Version: "1.0.0", Revision: "r2", Integrity: "sha256:2"},
		{ID: "d", Version: "1.0.0", Revision: "r1", Integrity: "sha256:1"},
	}}

	changes := LockChanges(current, candidate)
	kinds := map[string]string{}
	for _, change := range changes {
		kinds[change.PackID] = change.Kind
	}

	want := map[string]string{
		"a": "upgraded",
		"b": "downgraded",
		"c": "revised",
		"d": "added",
	}
	for id, wantKind := range want {
		if kinds[id] != wantKind {
			t.Errorf("package %s: kind = %q, want %q", id, kinds[id], wantKind)
		}
	}

	removed := LockChanges(current, LockFile{Packages: []LockedPackage{current.Packages[1], current.Packages[2]}})
	found := false
	for _, change := range removed {
		if change.PackID == "a" {
			found = true
			if change.Kind != "removed" {
				t.Errorf("package a: kind = %q, want %q", change.Kind, "removed")
			}
		}
	}
	if !found {
		t.Fatalf("expected a removed-package change for %q", "a")
	}
}
