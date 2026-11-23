package consumer

import (
	"path/filepath"
	"testing"
)

func TestUnregisterEnginePreservesReplacement(t *testing.T) {
	dir := t.TempDir()
	mount, alias := filepath.Join(dir, "mount"), filepath.Join(dir, "alias")
	original, replacement := &Engine{}, &Engine{}
	t.Cleanup(func() {
		UnregisterEngine(original)
		UnregisterEngine(replacement)
	})

	RegisterBranchEngine(mount, original)
	RegisterBranchEngine(alias, original)
	RegisterBranchEngine(mount, replacement)
	UnregisterEngine(original)

	if got, ok := LookupBranchEngine(mount); !ok || got != replacement {
		t.Fatal("unregistering the original engine removed the replacement")
	}
	if _, ok := LookupBranchEngine(alias); ok {
		t.Fatal("the original engine alias remains registered")
	}
}
