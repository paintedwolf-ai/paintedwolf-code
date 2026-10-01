package documentcore

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestChangedLinesNameRemovedLinesAndInsertionPoints(t *testing.T) {
	changes, err := ChangedLines("a\nb\nc\nd\ne\nf\n", "a\nB\nC\nd\ne\nnew\nf\n")
	testutil.FailErr(t, "diff lines", err)
	if len(changes) != 2 {
		t.Fatalf("changes = %+v", changes)
	}
	if got := changes[0]; got.StartLine != 2 || got.EndLine != 3 || got.Insertion || got.Added != 2 || got.Removed != 2 {
		t.Fatalf("replacement = %+v", got)
	}
	if got := changes[1]; !got.Insertion || got.StartLine != 6 || got.Added != 1 {
		t.Fatalf("insertion before f = %+v", got)
	}
	inserted, err := ChangedLines("a\nb\n", "a\nnew\nb\n")
	testutil.FailErr(t, "diff insertion", err)
	if len(inserted) != 1 || !inserted[0].Insertion || inserted[0].StartLine != 2 {
		t.Fatalf("insertion = %+v", inserted)
	}
}
