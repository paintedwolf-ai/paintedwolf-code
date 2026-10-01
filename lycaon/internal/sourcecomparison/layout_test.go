package sourcecomparison

import (
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"testing"
)

func TestProjectionDescribesUnloadedEnd(t *testing.T) {
	text := strings.Repeat("source line\n", 2400)
	document, err := New(api.SourceComparisonSide{Content: text}, api.SourceComparisonSide{Content: text}, nil)
	testutil.FailErr(t, "prepare projection fixture", err)
	projection, err := document.Project(t.Context(), "full", nil, nil)
	testutil.FailErr(t, "project full source", err)
	total, err := projection.Extent(t.Context())
	testutil.FailErr(t, "read extent", err)
	if total != 2400 || len(projection.spans) != 1 {
		t.Fatalf("extent=%d spans=%d", total, len(projection.spans))
	}
	frame := frameForTest(t, document, 2300, 100, "full")
	if !frame.Complete || len(frame.Rows) != 100 {
		t.Fatalf("end frame: %+v", frame)
	}
}

func TestProjectionKeepsCollapsedContextSeparate(t *testing.T) {
	before := strings.Repeat("context\n", 2000)
	after := strings.Repeat("context\n", 1000) + "changed\n" + strings.Repeat("context\n", 999)
	document, err := New(api.SourceComparisonSide{Content: before}, api.SourceComparisonSide{Content: after}, nil)
	testutil.FailErr(t, "prepare summarized projection", err)
	frame := frameForTest(t, document, 0, 200, "changes")
	if !frame.Complete || len(frame.Rows) < 3 || len(frame.Rows) > 20 || frame.Rows[0].Kind != "gap" || frame.Rows[len(frame.Rows)-1].Kind != "gap" {
		t.Fatalf("missing compact context: %+v", frame)
	}
}

func TestSplitProjectionReservesOnlyDisplayedRows(t *testing.T) {
	document, err := New(api.SourceComparisonSide{Content: strings.Repeat("removed\n", 1500)}, api.SourceComparisonSide{Content: strings.Repeat("added\n", 1500)}, nil)
	testutil.FailErr(t, "prepare paired comparison", err)
	projection, err := document.Project(t.Context(), "split", nil, nil)
	testutil.FailErr(t, "project split source", err)
	total, err := projection.Extent(t.Context())
	testutil.FailErr(t, "read split extent", err)
	rank, anchor, err := projection.Locate(t.Context(), 2400)
	testutil.FailErr(t, "locate peer", err)
	frame := frameForTest(t, document, int(rank), 1, "split")
	if total != 1500 || rank != 900 || anchor.Row != 900 || len(frame.Rows) != 1 || frame.Rows[0].Peer == nil || frame.Rows[0].Peer.Index != 2400 {
		t.Fatalf("paired extent=%d rank=%d frame=%+v", total, rank, frame)
	}
	end := frameForTest(t, document, 1499, 1, "split")
	if !end.Complete || end.End != 1500 {
		t.Fatalf("end frame: %+v", end)
	}
	continuation := frameForTest(t, document, end.End, 1, "split")
	if !continuation.Complete || len(continuation.Rows) != 0 {
		t.Fatalf("repeated peer: %+v", continuation)
	}
}
