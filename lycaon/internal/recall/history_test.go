package recall_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/recall"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestHistoryPagesCanonicalRowsWithoutAnIndex(t *testing.T) {
	f := newFixture(t)
	body := strings.Repeat("λ", 9000)
	for _, row := range []struct {
		id, session, content string
		ord                  int
	}{{"old", coordinatorSession, "earlier decision", 1}, {"large", coordinatorSession, body, 2}, {"tail", coordinatorSession, "latest", 3}, {"peer", workerSession, "worker secret", 1}} {
		_, err := f.db.ExecContext(t.Context(), `INSERT INTO messages (id,session_id,role,content,origin,authority,trust_tier,ord,seq,ts)
			VALUES (?,?,'assistant',?,'model','none','trusted',?,?,'2026-10-14T00:00:00Z')`, row.id, row.session, row.content, row.ord, row.ord)
		testutil.FailErr(t, "seed canonical history", err)
	}
	req := recall.Request{Caller: coordinator(), Limit: 1, History: &recall.HistoryRequest{ThroughOrd: 3, SourceSeq: 3}}
	first, err := f.svc.Answer(t.Context(), req)
	testutil.FailErr(t, "read index", err)
	if first.History.NextAfterOrd != 1 || first.History.Entries[0].ID != "old" {
		t.Fatalf("wrong first page: %+v", first)
	}
	req.History.AfterOrd = first.History.NextAfterOrd
	next, err := f.svc.Answer(t.Context(), req)
	testutil.FailErr(t, "continue index", err)
	if next.History.Entries[0].ID != "large" || next.History.NextAfterOrd != 2 {
		t.Fatal("ordinal seek skipped a row")
	}
	req.History.AfterOrd = 0
	req.History.MessageID = "large"
	var recovered strings.Builder
	for {
		result, err := f.svc.Answer(t.Context(), req)
		testutil.FailErr(t, "recover body window", err)
		recovered.WriteString(result.History.Entries[0].Content)
		if result.History.NextOffset == nil {
			break
		}
		req.History.Offset = *result.History.NextOffset
	}
	if recovered.String() != body {
		t.Fatal("body windows changed recorded Unicode content")
	}
	_, err = f.db.ExecContext(t.Context(), "UPDATE messages SET seq=4 WHERE id='large'")
	testutil.FailErr(t, "record source correction", err)
	changed, err := f.svc.Answer(t.Context(), req)
	testutil.FailErr(t, "read changed record", err)
	if !changed.History.SourceChanged {
		t.Fatal("correction did not invalidate the source watermark")
	}
	req.Caller = worker(workerSession)
	req.History.MessageID = "large"
	req.History.Offset = 0
	isolated, err := f.svc.Answer(t.Context(), req)
	testutil.FailErr(t, "worker history isolation", err)
	if len(isolated.History.Entries) != 0 {
		t.Fatal("worker recovered another session's body")
	}
	req.Widen = recall.WidenProject
	if _, err := f.svc.Answer(t.Context(), req); err == nil {
		t.Fatal("history widened a worker's scope")
	}
}
