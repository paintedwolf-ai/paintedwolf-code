package confine

import (
	"path/filepath"
	"testing"
)

func TestPrivateTempResolution(t *testing.T) {
	scratch := t.TempDir()
	alias := t.TempDir()
	mapping := PrivateTemp{Paths: []string{alias}, ScratchRoot: scratch}
	for _, tc := range []struct{ path, want string }{
		{alias, scratch},
		{filepath.Join(alias, "a", "b"), filepath.Join(scratch, "a", "b")},
		{alias + "-sibling", ""},
		{"relative", ""},
	} {
		got, ok := mapping.Resolve(tc.path)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("Resolve(%q) = (%q, %t), want %q", tc.path, got, ok, tc.want)
		}
	}
	if _, ok := (PrivateTemp{Paths: []string{alias}}).Resolve(alias); ok {
		t.Fatal("launch-local tmpfs resolved to a host path")
	}
}
