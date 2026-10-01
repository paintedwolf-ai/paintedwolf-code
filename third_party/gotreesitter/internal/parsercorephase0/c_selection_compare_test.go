package parsercorephase0

import (
	"errors"
	"testing"
)

func newCSelectionCompareTestCore(t *testing.T) *Core {
	t.Helper()
	compact, err := New(&fakeTable{}, Limits{MaxSubtrees: 32, MaxChildren: 32})
	if err != nil {
		t.Fatal(err)
	}
	return compact
}

func TestCompareCSelectionSubtreesUsesRawCOrder(t *testing.T) {
	compact := newCSelectionCompareTestCore(t)
	leaf2, err := compact.appendSubtree(subtreeRecord{symbol: 2}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	leaf3, err := compact.appendSubtree(subtreeRecord{symbol: 3}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	left, err := compact.appendSubtree(subtreeRecord{symbol: 10}, []SubtreeID{leaf2}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	right, err := compact.appendSubtree(subtreeRecord{symbol: 10}, []SubtreeID{leaf3}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if got, err := compact.CompareCSelectionSubtrees(left, right); err != nil || got != -1 {
		t.Fatalf("recursive comparison=(%d, %v), want (-1, nil)", got, err)
	}
	if got, err := compact.CompareCSelectionSubtrees(right, left); err != nil || got != 1 {
		t.Fatalf("reverse comparison=(%d, %v), want (1, nil)", got, err)
	}
	if got, err := compact.CompareCSelectionSubtrees(left, left); err != nil || got != 0 {
		t.Fatalf("identity comparison=(%d, %v), want (0, nil)", got, err)
	}
}

func TestCompareCSelectionSubtreesUsesChildCountBeforeChildren(t *testing.T) {
	compact := newCSelectionCompareTestCore(t)
	leaf, err := compact.appendSubtree(subtreeRecord{symbol: 2}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	short, err := compact.appendSubtree(subtreeRecord{symbol: 10}, []SubtreeID{leaf}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	long, err := compact.appendSubtree(subtreeRecord{symbol: 10}, []SubtreeID{leaf, leaf}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := compact.CompareCSelectionSubtrees(short, long); err != nil || got != -1 {
		t.Fatalf("child-count comparison=(%d, %v), want (-1, nil)", got, err)
	}
}

func TestCompareCSelectionSubtreesIgnoresNonCIdentityFields(t *testing.T) {
	compact := newCSelectionCompareTestCore(t)
	left, err := compact.appendSubtree(subtreeRecord{
		symbol: 4, productionID: 1, dynamicPrecedence: -3,
		startByte: 1, endByte: 2, extra: true, terminal: true,
	}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	right, err := compact.appendSubtree(subtreeRecord{
		symbol: 4, productionID: 9, dynamicPrecedence: 7,
		startByte: 20, endByte: 30, missing: true,
	}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := compact.CompareCSelectionSubtrees(left, right); err != nil || got != 0 {
		t.Fatalf("non-C identity comparison=(%d, %v), want (0, nil)", got, err)
	}
}

func TestCompareCSelectionSubtreesBoundsRepeatedDAGWork(t *testing.T) {
	compact := newCSelectionCompareTestCore(t)
	left, err := compact.appendSubtree(subtreeRecord{symbol: 2}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	right, err := compact.appendSubtree(subtreeRecord{symbol: 2}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for level := 0; level < 6; level++ {
		left, err = compact.appendSubtree(subtreeRecord{symbol: 10}, []SubtreeID{left, left}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		right, err = compact.appendSubtree(subtreeRecord{symbol: 10}, []SubtreeID{right, right}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := compact.CompareCSelectionSubtrees(left, right); !errors.Is(err, ErrCSelectionComparisonBudget) {
		t.Fatalf("repeated DAG comparison error=%v, want work-budget error", err)
	}
}
