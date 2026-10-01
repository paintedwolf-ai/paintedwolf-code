package checkpoint

import (
	"strconv"
	"testing"
)

func TestMutationLedgerReportsOnlyFirstTouchPerInterval(t *testing.T) {
	l := &mutationLedger{}
	if !firstTouch(l, "sess", "src/foo.go") {
		t.Fatal("first touch must report first=true so the pre-image is captured")
	}
	if firstTouch(l, "sess", "src/foo.go") {
		t.Fatal("repeat touch must report first=false — later writes are the turn's own bytes")
	}
	if !firstTouch(l, "other", "src/foo.go") {
		t.Fatal("a different session is a different interval")
	}
}

func TestMutationLedgerNormalizesAndRejectsEscapes(t *testing.T) {
	l := &mutationLedger{}
	if !firstTouch(l, "sess", "./src/../src/foo.go") {
		t.Fatal("normalized path should record")
	}
	if firstTouch(l, "sess", "src/foo.go") {
		t.Fatal("normalization must collapse to the same key")
	}
	if firstTouch(l, "sess", "../outside.go") {
		t.Fatal("a path escaping the root must never enter the ledger")
	}
}

func TestMutationLedgerClearStartsANewInterval(t *testing.T) {
	l := &mutationLedger{}
	firstTouch(l, "sess", "src/foo.go")
	l.Clear("sess")
	if !firstTouch(l, "sess", "src/foo.go") {
		t.Fatal("after a seal, the next turn's first touch must capture a fresh pre-image")
	}
}

// Past the cap the ledger stops capturing and reports overflow exactly once, so
// the checkpoint marks itself truncated instead of restoring a partial turn.
func TestMutationLedgerAnnouncesOverflowOnceAndStopsGrowing(t *testing.T) {
	l := &mutationLedger{}
	for i := 0; i < MaxPaths; i++ {
		first, overflowed := l.RecordPrimaryTouch("sess", "src/f"+strconv.Itoa(i)+".go")
		if !first || overflowed {
			t.Fatalf("path %d: first=%v overflowed=%v, want true/false", i, first, overflowed)
		}
	}
	first, overflowed := l.RecordPrimaryTouch("sess", "src/over-1.go")
	if first || !overflowed {
		t.Fatalf("first past the cap: first=%v overflowed=%v, want false/true", first, overflowed)
	}
	// Latched: the manifest is marked once, not on every later write.
	for i := 2; i < 6; i++ {
		first, overflowed := l.RecordPrimaryTouch("sess", "src/over-"+strconv.Itoa(i)+".go")
		if first || overflowed {
			t.Fatalf("later over-cap write: first=%v overflowed=%v, want false/false", first, overflowed)
		}
	}
	// A new interval starts clean, including the latch.
	l.Clear("sess")
	if first, _ := l.RecordPrimaryTouch("sess", "src/f0.go"); !first {
		t.Fatal("after Clear the next turn must capture fresh pre-images")
	}
}

// firstTouch keeps the single-fact cases readable; overflow has its own test.
func firstTouch(l *mutationLedger, sessionID, relPath string) bool {
	first, _ := l.RecordPrimaryTouch(sessionID, relPath)
	return first
}
