package pathfold

import "testing"

func TestFoldMergesTheDeepestLevelFirst(t *testing.T) {
	paths := map[string]int{"a/x/1.go": 1, "a/x/2.go": 2, "a/y/3.go": 3, "b.go": 4}
	folded, ok := Fold(paths, 3, larger)
	if !ok || len(folded) != 3 || folded["a/x"] != 2 || folded["a/y"] != 3 || folded["b.go"] != 4 {
		t.Fatalf("folded = %v, %v", folded, ok)
	}
	folded, ok = Fold(paths, 2, larger)
	if !ok || len(folded) != 2 || folded["a"] != 3 || folded["b.go"] != 4 {
		t.Fatalf("folded twice = %v, %v", folded, ok)
	}
}

func TestFoldRefusesToFoldIntoTheRoot(t *testing.T) {
	if _, ok := Fold(map[string]int{"a.go": 1, "b.go": 2}, 1, larger); ok {
		t.Fatal("top-level paths folded into the root")
	}
	if folded, ok := Fold(map[string]int{"a.go": 1}, 1, larger); !ok || len(folded) != 1 {
		t.Fatalf("a set within its limit changed: %v, %v", folded, ok)
	}
}

func larger(a, b int) int { return max(a, b) }
