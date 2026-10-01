package contribution

import "testing"

// Identity groups collisions; binding_defaults publishes the canonical form in
// the modifier order the dispatcher and keycap UI use.
func TestCanonicalChordIsNotTheIdentitySortKey(t *testing.T) {
	t.Parallel()

	cases := []struct{ binding, identity, canonical string }{
		{"Mod+Alt+D", "Alt+Mod+D", "Mod+Alt+D"},
		{"Alt+Mod+D", "Alt+Mod+D", "Mod+Alt+D"},
		{"Mod+Shift+L", "Mod+Shift+L", "Mod+Shift+L"},
		{"Shift+Mod+L", "Mod+Shift+L", "Mod+Shift+L"},
		{"Mod+Alt+Shift+N", "Alt+Mod+Shift+N", "Mod+Alt+Shift+N"},
		{"Escape", "Escape", "Escape"},
		{"Leader F", "Leader F", "Leader F"},
	}
	for _, tc := range cases {
		if got := chordIdentity(tc.binding); got != tc.identity {
			t.Errorf("chordIdentity(%q) = %q, want %q", tc.binding, got, tc.identity)
		}
		if got := canonicalChord(tc.binding); got != tc.canonical {
			t.Errorf("canonicalChord(%q) = %q, want %q", tc.binding, got, tc.canonical)
		}
	}
}
