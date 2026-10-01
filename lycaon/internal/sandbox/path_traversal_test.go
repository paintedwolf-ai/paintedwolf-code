package sandbox

import "testing"

func TestHasParentTraversalUsesPathComponents(t *testing.T) {
	for _, path := range []string{"../outside", "src/../../outside", `src\..\outside`} {
		if !HasParentTraversal(path) {
			t.Fatalf("HasParentTraversal(%q) = false", path)
		}
	}
	for _, path := range []string{"notes..draft.md", "cache..snapshot/file", ".../file"} {
		if HasParentTraversal(path) {
			t.Fatalf("HasParentTraversal(%q) = true", path)
		}
	}
}
