package editordoc

import (
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestAgentSpansHoldUntilTheirTextChanges(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "one\ntwo\nthree\nfour\n"})
	doc := f.open(t, "a.txt")
	anchored, err := f.service.AnchorSpans(t.Context(), doc.ProjectID, doc.ID, doc.Revision, []TextSpan{{StartLine: 2, EndLine: 3}, {StartLine: 4, EndLine: 4}})
	testutil.FailErr(t, "anchor read spans", err)
	if len(anchored.Spans) != 2 || anchored.Spans[0].Expected != "two\nthree" || !anchored.Spans[0].Checkable || anchored.Epoch < 1 {
		t.Fatalf("anchored spans = %+v", anchored)
	}
	edited, err := f.service.ReplaceSnapshot(t.Context(), doc.ID, doc.ProjectID, SnapshotReplacement{
		DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: doc.Revision},
		Content:         "zero\none\ntwo\nthree\nFOUR\n", EOL: "lf"})
	testutil.FailErr(t, "edit above and inside", err)
	held, err := f.service.SpansHold(t.Context(), edited.ProjectID, edited.ID, anchored.Spans)
	testutil.FailErr(t, "check spans", err)
	if !held[0] || held[1] {
		t.Fatalf("held = %v, want the moved span to hold and the edited span to change", held)
	}
}

func TestAgentSpansAnchorTheRevisionTheAgentRead(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "alpha\nbeta\n"})
	opened := f.open(t, "a.txt")
	// Agent reads pin the revision they return, as OpenText does.
	read, err := f.service.Pin(t.Context(), opened.ProjectID, opened.ID, opened.Revision)
	testutil.FailErr(t, "pin the agent read", err)
	current, err := f.service.ReplaceSnapshot(t.Context(), read.ID, read.ProjectID, SnapshotReplacement{
		DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: read.Revision},
		Content:         "inserted\nalpha\nbeta\n", EOL: "lf"})
	testutil.FailErr(t, "edit after the read", err)
	anchored, err := f.service.AnchorSpans(t.Context(), read.ProjectID, read.ID, read.Revision, []TextSpan{{StartLine: 2, EndLine: 2}})
	testutil.FailErr(t, "anchor the read revision", err)
	if anchored.Revision != read.Revision || anchored.Spans[0].Expected != "beta" {
		t.Fatalf("anchored = %+v, want line 2 of the read revision", anchored)
	}
	held, err := f.service.SpansHold(t.Context(), current.ProjectID, current.ID, anchored.Spans)
	testutil.FailErr(t, "check against the current head", err)
	if !held[0] {
		t.Fatal("span anchored in the read revision did not follow the later edit")
	}
}

func TestAgentSpansRejectRangesOutsideTheText(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "only\n"})
	doc := f.open(t, "a.txt")
	for _, span := range []TextSpan{{StartLine: 0, EndLine: 1}, {StartLine: 3, EndLine: 3}, {StartLine: 2, EndLine: 1}, {StartLine: 1, EndLine: 1, StartCharacter: intPointer(9)}} {
		if _, err := f.service.AnchorSpans(t.Context(), doc.ProjectID, doc.ID, 0, []TextSpan{span}); err == nil {
			t.Fatalf("span %+v anchored", span)
		}
	}
}

func TestAgentPathSpansNeverOpenADocument(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "a\n", "b.txt": "b\n"})
	doc := f.open(t, "a.txt")
	root := doc.RootID
	if _, ok, err := f.service.AnchorPathSpans(t.Context(), doc.ProjectID, root, "b.txt", []TextSpan{{StartLine: 1, EndLine: 1}}); err != nil || ok {
		t.Fatalf("unopened path anchored: ok=%v err=%v", ok, err)
	}
	anchored, ok, err := f.service.AnchorPathSpans(t.Context(), doc.ProjectID, root, "a.txt", []TextSpan{{StartLine: 1, EndLine: 1}})
	testutil.FailErr(t, "anchor an existing document", err)
	if !ok || anchored.DocumentID != doc.ID {
		t.Fatalf("existing document anchor = %+v ok=%v", anchored, ok)
	}
}

func intPointer(v int) *int { return &v }
