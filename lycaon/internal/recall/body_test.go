package recall_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/recall"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestHandleRecallLoadsPreservedBodyBeyondIndexedExcerpt(t *testing.T) {
	f := newFixture(t)
	st := store.NewSQL(f.db)
	st.SetDataDir(f.dataDir)
	body := []string{"first line", "historical retry delay: 7119", "last line"}
	testutil.FailErr(t, "store observation", st.UpsertEvidenceRecord(t.Context(), workerSession, evidence.Record{
		Handle: "read#1", Kind: "read", Path: "settings.json", Body: body,
	}))
	f.seed(t, evidenceRow("observed", workerSession, "read#1", "settings.json", "first line"))
	result := f.answer(t, coordinator(), "handle:read#1", recall.WidenDefault)
	if result.Resolution != recall.ResolutionMatched || len(result.Hits) != 1 || !reflect.DeepEqual(result.Hits[0].Body, body) {
		t.Fatalf("preserved observation missing: %+v", result)
	}
	if result.Hits[0].BodyTruncated || len(result.Issues) != 0 {
		t.Fatalf("complete body reported partial: %+v", result)
	}
	filtered := f.answer(t, coordinator(), "handle:read#1 AND 7119", recall.WidenDefault)
	if filtered.Resolution != recall.ResolutionNoMatchInScope {
		t.Fatalf("text must retain indexed-query semantics: %+v", filtered)
	}
}

func TestRecallBodyBoundsAndStorageFailure(t *testing.T) {
	f := newFixture(t)
	st := store.NewSQL(f.db)
	st.SetDataDir(f.dataDir)
	body := strings.Split(strings.Repeat("line\n", recall.BodyMaxLines+1), "\n")
	testutil.FailErr(t, "store observation", st.UpsertEvidenceRecord(t.Context(), workerSession, evidence.Record{
		Handle: "read#1", Kind: "read", Body: body,
	}))
	f.seed(t, evidenceRow("observed", workerSession, "read#1", "", "line"))
	result := f.answer(t, coordinator(), "handle:read#1", recall.WidenDefault)
	if len(result.Hits) != 1 || len(result.Hits[0].Body) != recall.BodyMaxLines || !result.Hits[0].BodyTruncated {
		t.Fatalf("body display bound missing: %+v", result)
	}
	// A different host-data root makes the indexed blob unavailable without
	// altering the source observation or its metadata.
	f.svc = recall.NewService(f.db, t.TempDir())
	result = f.answer(t, coordinator(), "handle:read#1", recall.WidenDefault)
	if result.Resolution != recall.ResolutionExecutorDegraded || len(result.Issues) == 0 || len(result.Hits) != 1 {
		t.Fatalf("body storage failure disguised as a match: %+v", result)
	}
	if len(result.Hits[0].Body) != 0 || result.NextAction == "" {
		t.Fatalf("degraded answer must preserve excerpt and state recovery: %+v", result)
	}
}
