package recall_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/recall"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestHistoryRecordPreservesArgumentsAndPartsWithoutPrivateReplay(t *testing.T) {
	f := newFixture(t)
	testdbseed.InsertSessionEntry(t, f.db, "call", coordinatorSession, "model_output", "call", 1)
	_, err := f.db.ExecContext(t.Context(), `INSERT INTO messages (entry_id,id,session_id,role,content,tool_calls_json,content_parts_json,origin,authority,trust_tier,ord,seq,ts)
	VALUES ('call','call',?,'assistant','',?,?,'model','none','untrusted',1,1,'2026-10-14T00:00:00Z')`, coordinatorSession,
		`[{"id":"original-call","name":"read","args":{"path":"original.go"},"wire_id":"private-id","extra_content":{"reasoning":"private replay"}}]`,
		`[{"content":"recorded λ plan","origin":"model","authority":"none","trust_tier":"untrusted"}]`)
	testutil.FailErr(t, "seed original call", err)
	history := &recall.HistoryRequest{ThroughOrd: 1, MessageID: "call", Format: "record", BodyRunes: 32}
	var record strings.Builder
	for {
		result, err := f.svc.Answer(t.Context(), recall.Request{Caller: coordinator(), History: history})
		testutil.FailErr(t, "recover record page", err)
		if result.History.Format != "record" {
			t.Fatal("record page lost its format")
		}
		record.WriteString(result.History.Entries[0].Content)
		if result.History.NextOffset == nil {
			break
		}
		history.Offset = *result.History.NextOffset
	}
	var body struct {
		ToolCalls []struct {
			Args map[string]string `json:"args"`
		} `json:"tool_calls"`
		Parts []struct{ Content string } `json:"content_parts"`
	}
	testutil.FailErr(t, "decode original record", json.Unmarshal([]byte(record.String()), &body))
	if body.ToolCalls[0].Args["path"] != "original.go" || body.Parts[0].Content != "recorded λ plan" || strings.Contains(record.String(), "private") {
		t.Fatal("record recovery lost public fields or disclosed private replay")
	}
}

func TestHistoryPagesCanonicalRowsWithoutAnIndex(t *testing.T) {
	f := newFixture(t)
	body := strings.Repeat("λ", 9000)
	for _, row := range []struct {
		id, session, content string
		ord                  int
	}{{"old", coordinatorSession, "earlier decision", 1}, {"large", coordinatorSession, body, 2}, {"tail", coordinatorSession, "latest", 3}, {"peer", workerSession, "worker secret", 1}} {
		testdbseed.InsertSessionEntry(t, f.db, row.id, row.session, "model_output", row.id, int64(row.ord))
		_, err := f.db.ExecContext(t.Context(), `INSERT INTO messages (entry_id,id,session_id,role,content,origin,authority,trust_tier,ord,seq,ts)
			VALUES (?,?,?,'assistant',?,'model','none','trusted',?,?,'2026-10-14T00:00:00Z')`, row.id, row.id, row.session, row.content, row.ord, row.ord)
		testutil.FailErr(t, "seed canonical history", err)
	}
	req := recall.Request{Caller: coordinator(), Limit: 1, History: &recall.HistoryRequest{ThroughOrd: 3, SourceSeq: 3}}
	first, err := f.svc.Answer(t.Context(), req)
	testutil.FailErr(t, "read index", err)
	if first.History.NextAfterOrd != 1 || first.History.Entries[0].MessageID != "old" {
		t.Fatalf("wrong first page: %+v", first)
	}
	req.History.AfterOrd = first.History.NextAfterOrd
	next, err := f.svc.Answer(t.Context(), req)
	testutil.FailErr(t, "continue index", err)
	if next.History.Entries[0].MessageID != "large" || next.History.NextAfterOrd != 2 {
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

func TestHistoryValidatesCoordinatesAndCanonicalScope(t *testing.T) {
	f := newFixture(t)
	for _, history := range []recall.HistoryRequest{{}, {ThroughOrd: 2, AfterOrd: 2}, {ThroughOrd: 2, AfterOrd: -1}, {ThroughOrd: 2, Offset: -1}, {ThroughOrd: 2, SourceSeq: -1}} {
		_, err := f.svc.Answer(t.Context(), recall.Request{Caller: coordinator(), History: &history})
		var invalid *recall.ErrHistoryCoordinates
		if !errors.As(err, &invalid) {
			t.Fatalf("invalid coordinates returned %v", err)
		}
	}
	req := recall.Request{Caller: coordinator(), History: &recall.HistoryRequest{ThroughOrd: 2}}
	empty, err := f.svc.Answer(t.Context(), req)
	testutil.FailErr(t, "empty canonical session", err)
	if empty.Resolution != recall.ResolutionScopeEmpty {
		t.Fatal("empty history claimed unmatched observations")
	}
	req.Caller.ProjectID = "another-project"
	_, err = f.svc.Answer(t.Context(), req)
	var denied *recall.ErrScopeDenied
	if !errors.As(err, &denied) {
		t.Fatalf("project mismatch returned %v", err)
	}
	req.Caller = recall.Caller{SessionID: "removed", ProjectID: projectID}
	removed, err := f.svc.Answer(t.Context(), req)
	testutil.FailErr(t, "removed session", err)
	if removed.Resolution != recall.ResolutionRecordDeleted {
		t.Fatal("missing session was not reported")
	}
}

func TestHistorySearchFindsOriginalInstructionsWithinCallerScope(t *testing.T) {
	f := newFixture(t)
	for _, row := range []struct{ id, session, content string }{
		{"instruction", coordinatorSession, "Keep golden otter releases behind human approval."},
		{"peer", workerSession, "Golden otter worker assignment"},
	} {
		testdbseed.InsertSessionEntry(t, f.db, row.id, row.session, "utterance", row.id, 1)
		_, err := f.db.ExecContext(t.Context(), `INSERT INTO messages (entry_id,id,session_id,role,content,origin,authority,trust_tier,ord,seq,ts)
		VALUES (?,?,?,'user',?,'user','user','trusted',1,1,'2026-10-14T00:00:00Z')`, row.id, row.id, row.session, row.content)
		testutil.FailErr(t, "seed original instruction", err)
	}
	req := recall.Request{Caller: coordinator(), History: &recall.HistoryRequest{ThroughOrd: 1, Search: "golden otter"}}
	result, err := f.svc.Answer(t.Context(), req)
	testutil.FailErr(t, "search recorded instructions", err)
	if len(result.History.Entries) != 1 || result.History.Entries[0].MessageID != "instruction" {
		t.Fatal("history search lost the original instruction or widened scope")
	}
	for _, search := range []string{strings.Repeat("x", 257), "a b c d e f g h i"} {
		req.History.Search = search
		_, err := f.svc.Answer(t.Context(), req)
		var invalid *recall.ErrHistoryCoordinates
		if !errors.As(err, &invalid) {
			t.Fatal("history search admitted an unbounded query")
		}
	}
}

func TestHistoryRecoversRecordedToolBodiesAndHandles(t *testing.T) {
	f := newFixture(t)
	testdbseed.InsertSessionEntry(t, f.db, "tool", coordinatorSession, "tool_receipt", "tool", 1)
	_, err := f.db.ExecContext(t.Context(), `INSERT INTO messages (entry_id,id,session_id,role,content,tool_result_json,evidence_handles_json,origin,authority,trust_tier,ord,seq,ts)
	VALUES ('tool','tool',?,'tool','',?,?,'tool','none','untrusted',1,1,'2026-10-14T00:00:00Z')`, coordinatorSession, `{"tool":"read","content":"recorded bytes"}`, `["read#3"]`)
	testutil.FailErr(t, "seed tool body", err)
	result, err := f.svc.Answer(t.Context(), recall.Request{Caller: coordinator(), History: &recall.HistoryRequest{ThroughOrd: 1, MessageID: "tool"}})
	testutil.FailErr(t, "recover tool body", err)
	entry := result.History.Entries[0]
	if entry.Content != "recorded bytes" || entry.Handles[0] != "read#3" || result.History.NextOffset != nil {
		t.Fatal("recorded tool recovery lost its body or handle")
	}
}

func TestHistoryDoesNotExposeInternalOrUncommittedRows(t *testing.T) {
	f := newFixture(t)
	for _, row := range []struct {
		id, visibility, kind, status string
		ord                          int
	}{
		{"internal", "internal", "normal", "", 1}, {"draft", "transcript", "draft", "draft", 2},
	} {
		testdbseed.InsertSessionEntry(t, f.db, row.id, coordinatorSession, "model_output", row.id, int64(row.ord))
		_, err := f.db.ExecContext(t.Context(), `INSERT INTO messages (entry_id,id,session_id,role,content,visibility,kind,draft_status,origin,authority,trust_tier,ord,seq,ts)
		VALUES (?,?,?,'assistant','hidden',?,?,?,'model','none','trusted',?,?,'2026-10-14T00:00:00Z')`, row.id, row.id, coordinatorSession, row.visibility, row.kind, row.status, row.ord, row.ord)
		testutil.FailErr(t, "seed hidden rows", err)
	}
	for _, id := range []string{"", "internal", "draft"} {
		result, err := f.svc.Answer(t.Context(), recall.Request{Caller: coordinator(), History: &recall.HistoryRequest{ThroughOrd: 2, MessageID: id}})
		testutil.FailErr(t, "recover visible transcript only", err)
		if len(result.History.Entries) != 0 || result.Resolution != recall.ResolutionScopeEmpty {
			t.Fatal("history exposed a row outside the transcript")
		}
	}
}
