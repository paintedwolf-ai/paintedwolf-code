package parsercorephase0

import (
	"reflect"
	"strings"
	"testing"
)

func newEOFAdmissionViewFixture(t *testing.T) (*Core, Head, SubtreeID, []SubtreeID) {
	t.Helper()
	compact, err := New(&fakeTable{}, Limits{
		MaxNodes: 64, MaxLinks: 64, MaxSubtrees: 64, MaxChildren: 64,
		MaxMetadata: 64, MaxLinksPerBoundary: 8, MaxPopPaths: 8, MaxDerivations: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := compact.Seed(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	left, err := compact.appendSubtreeRecord(subtreeRecord{
		symbol: 10, startByte: 0, endByte: 1, terminal: true,
	}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	right, err := compact.appendSubtreeRecord(subtreeRecord{
		symbol: 11, startByte: 1, endByte: 2, terminal: true,
	}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	children := []SubtreeID{left, right}
	parent, err := compact.appendSubtreeRecord(subtreeRecord{
		symbol: 20, productionID: 7, dynamicPrecedence: 3, startByte: 0, endByte: 2,
	}, children, []FieldMapEntry{{FieldID: 4, ChildIndex: 1}}, []Symbol{0, 30})
	if err != nil {
		t.Fatal(err)
	}
	head, err := compact.condense(compact.boundaryKey(2, 2), linkInput{
		prev: seed.Node, payload: parent, scoreDelta: 5,
		order: ForkOrder{Value: 8, Present: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	tail, err := compact.appendSubtreeRecord(subtreeRecord{
		symbol: 12, startByte: 2, endByte: 3, terminal: true,
	}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	head, err = compact.condense(compact.boundaryKey(3, 3), linkInput{
		prev: head.Node, payload: tail, scoreDelta: -2,
	})
	if err != nil {
		t.Fatal(err)
	}
	return compact, head, parent, []SubtreeID{parent, tail}
}

func TestEOFAdmissionViewAndExactPathCursor(t *testing.T) {
	compact, head, parent, wantPayloads := newEOFAdmissionViewFixture(t)
	generation := compact.AuthenticationGeneration()
	var got EOFAdmissionSubtreeView
	if err := compact.VisitEOFAdmissionSubtree(parent, generation, func(view EOFAdmissionSubtreeView) error {
		got = view
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got.Generation != generation || got.Identity != parent || got.Symbol != 20 ||
		got.ProductionID != 7 || got.DynamicPrecedence != 3 || got.StartByte != 0 || got.EndByte != 2 ||
		got.Extra || got.External || got.Terminal || got.Fragile || got.Missing ||
		!reflect.DeepEqual(got.Children, []SubtreeID{1, 2}) ||
		!reflect.DeepEqual(got.Fields, []FieldMapEntry{{FieldID: 4, ChildIndex: 1}}) ||
		!reflect.DeepEqual(got.Aliases, []Symbol{0, 30}) {
		t.Fatalf("borrowed view=%+v", got)
	}

	var payloads []SubtreeID
	polls := 0
	path, err := compact.VisitEOFAdmissionExactPath(
		head,
		generation,
		func() error { polls++; return nil },
		func(_ uint32, payload SubtreeID) error {
			payloads = append(payloads, payload)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(payloads, wantPayloads) || path.Payloads != 2 || path.Links != 2 ||
		path.Score != 3 || !path.HasBranchOrder || path.BranchOrder != 8 ||
		path.Polls != uint32(polls) || polls != 3 {
		t.Fatalf("path=%+v payloads=%v polls=%d", path, payloads, polls)
	}
}

func TestEOFAdmissionExactPathCursorAllocatesZero(t *testing.T) {
	compact, head, _, _ := newEOFAdmissionViewFixture(t)
	generation := compact.AuthenticationGeneration()
	poll := func() error { return nil }
	visit := func(uint32, SubtreeID) error { return nil }
	if _, err := compact.VisitEOFAdmissionExactPath(head, generation, poll, visit); err != nil {
		t.Fatal(err)
	}
	allocations := testing.AllocsPerRun(100, func() {
		if _, err := compact.VisitEOFAdmissionExactPath(head, generation, poll, visit); err != nil {
			panic(err)
		}
	})
	if allocations != 0 {
		t.Fatalf("exact path cursor allocations=%v, want 0", allocations)
	}
}

func TestEOFAdmissionViewRejectsMalformedRangesAndGenerationChanges(t *testing.T) {
	t.Run("alias-range", func(t *testing.T) {
		compact, _, parent, _ := newEOFAdmissionViewFixture(t)
		compact.subtrees[parent-1].firstAlias = uint32(len(compact.aliases) + 1)
		err := compact.VisitEOFAdmissionSubtree(
			parent,
			compact.AuthenticationGeneration(),
			func(EOFAdmissionSubtreeView) error { return nil },
		)
		if err == nil || !strings.Contains(err.Error(), "alias range") {
			t.Fatalf("malformed alias range error=%v", err)
		}
	})

	t.Run("subtree-callback-generation", func(t *testing.T) {
		compact, _, parent, _ := newEOFAdmissionViewFixture(t)
		generation := compact.AuthenticationGeneration()
		err := compact.VisitEOFAdmissionSubtree(parent, generation, func(EOFAdmissionSubtreeView) error {
			return compact.BeginFrontier()
		})
		if err == nil || !strings.Contains(err.Error(), "generation changed") {
			t.Fatalf("callback generation error=%v", err)
		}
	})

	t.Run("path-poll-generation", func(t *testing.T) {
		compact, head, _, _ := newEOFAdmissionViewFixture(t)
		generation := compact.AuthenticationGeneration()
		mutated := false
		_, err := compact.VisitEOFAdmissionExactPath(
			head,
			generation,
			func() error {
				if mutated {
					return nil
				}
				mutated = true
				return compact.BeginFrontier()
			},
			func(uint32, SubtreeID) error { return nil },
		)
		if err == nil || !strings.Contains(err.Error(), "generation changed") {
			t.Fatalf("poll generation error=%v", err)
		}
	})
}
