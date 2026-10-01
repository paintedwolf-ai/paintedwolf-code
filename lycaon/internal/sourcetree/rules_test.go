package sourcetree

import (
	"fmt"
	"slices"
	"testing"
)

func TestDisclosurePreservesRecursiveIntent(t *testing.T) {
	root := Address{Root: "root", Path: "."}
	child := Address{Root: "root", Path: "src"}
	rules := Rules{}
	rules.Set(root, Disclosure{Open: true, Recursive: true})
	rules.Set(root, Disclosure{Open: false})
	rules.Set(child, Disclosure{Open: false})
	if rules.At(root).Open || rules.At(child).Open {
		t.Fatal("mutation erased a disclosure exception")
	}
	rules.Set(root, Disclosure{Open: true})
	if rule := rules.At(Address{Root: "root", Path: "other"}); !rule.Open || !rule.Recursive {
		t.Fatalf("reopening lost recursive intent: %+v", rule)
	}
	if rules.At(child).Open {
		t.Fatal("reopening erased a descendant exception")
	}
	rules.Set(root, Disclosure{Open: false, Recursive: true})
	rules.Set(root, Disclosure{Open: true})
	if rules.At(child).Open {
		t.Fatal("recursive collapse retained recursive expansion")
	}
}

func TestSparseBranchesFollowMutationsAndClone(t *testing.T) {
	rules := Rules{}
	root := Address{Root: "root", Path: "."}
	rules.Set(Address{Root: "root", Path: "a/b/c"}, Disclosure{Open: true})
	if branches := rules.Branches(root); len(branches) != 1 || branches[0] != "a" {
		t.Fatalf("branches=%v", branches)
	}
	clone := rules.Clone()
	rules.Set(root, Disclosure{Recursive: true})
	if len(rules.Branches(root)) != 0 || len(clone.Branches(root)) != 1 {
		t.Fatal("branch index survived mutation or leaked into clone")
	}
}

func TestDisclosureBranchesPreservePunctuationSiblings(t *testing.T) {
	rules := Rules{}
	for _, name := range []string{"a", "a-b/nested", "a/b", "a.b", "a0/child", ".hidden/x"} {
		rules.Set(Address{Root: "root", Path: name}, Disclosure{Open: true})
	}
	got := rules.Branches(Address{Root: "root", Path: "."})
	slices.Sort(got)
	if !slices.Equal(got, []string{".hidden", "a", "a-b", "a.b", "a0"}) {
		t.Fatalf("branches=%v", got)
	}
}

func TestRecursiveDisclosureDropsExceptionsWithoutChangingSnapshot(t *testing.T) {
	rules := Rules{}
	for i := range 10000 {
		rules.Set(Address{Root: "root", Path: fmt.Sprintf("src/dir-%05d/deep", i)}, Disclosure{Open: true})
	}
	original := rules.Clone()
	rules.Set(Address{Root: "root", Path: "src"}, Disclosure{Open: true, Recursive: true})
	if rules.count() != 1 || original.count() != 10000 {
		t.Fatalf("counts=%d/%d", rules.count(), original.count())
	}
	if original.At(Address{Root: "root", Path: "src"}).Open {
		t.Fatal("recursive command mutated retained rules")
	}
	if !rules.At(Address{Root: "root", Path: "src/anything"}).Recursive {
		t.Fatal("recursive rule was not inherited")
	}
}

func TestDerivedBoundariesYieldToIntent(t *testing.T) {
	rules := Rules{}
	root := Address{Root: "root", Path: "."}
	build := Address{Root: "root", Path: "build"}
	inner := Address{Root: "root", Path: "build/x"}
	rules.Set(root, Disclosure{Open: true, Recursive: true})
	rules.reconcileDerived(map[Address]bool{build: true})
	if rule := rules.At(build); rule.Open || !rule.Explicit {
		t.Fatalf("boundary rule = %+v, want closed and explicit", rule)
	}
	if rule := rules.At(inner); rule.Open || rule.Recursive {
		t.Fatalf("descendant of a boundary inherited the recursive rule: %+v", rule)
	}
	if rules.count() != 1 || rules.derivedCount() != 1 {
		t.Fatalf("count=%d derived=%d, want the boundary outside the intent count", rules.count(), rules.derivedCount())
	}
	if branches := rules.Branches(root); len(branches) != 1 || branches[0] != "build" {
		t.Fatalf("branches=%v, want the boundary as an exception", branches)
	}
	// Opening the boundary shows one level, the way any folder opens.
	rules.Set(build, Disclosure{Open: true})
	if !rules.At(build).Open || rules.At(inner).Open {
		t.Fatal("opening a boundary did not open exactly one level")
	}
	if len(rules.Derived()) != 0 {
		t.Fatal("intent at the boundary left a derived rule behind")
	}
	rules.reconcileDerived(map[Address]bool{build: true})
	if !rules.At(build).Open || len(rules.Derived()) != 0 {
		t.Fatal("derivation overrode intent at the boundary")
	}
	// Recursive intent at the anchor clears everything beneath, including the
	// opened boundary, so derivation closes it again.
	rules.Set(root, Disclosure{Open: true, Recursive: true})
	if rules.At(build).Explicit {
		t.Fatal("recursive intent kept the opened boundary")
	}
	rules.reconcileDerived(map[Address]bool{build: true})
	if rules.At(build).Open {
		t.Fatal("boundary stayed open after expanding all again")
	}
	rules.reconcileDerived(map[Address]bool{})
	if len(rules.Derived()) != 0 || rules.At(build).Explicit {
		t.Fatal("stale derived rule survived reconciliation")
	}
}
