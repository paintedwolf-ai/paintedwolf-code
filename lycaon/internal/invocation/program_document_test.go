package invocation_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type receiptRecord struct {
	Type           string                       `json:"type"`
	Program        wire.CommandProgramReceipt   `json:"program"`
	Progress       json.RawMessage              `json:"progress"`
	Activity       *wire.CommandActivity        `json:"activity"`
	ExecutionCount int                          `json:"execution_count"`
	NodeCount      int                          `json:"node_count"`
	ProgramID      string                       `json:"program_id"`
	ActivityID     string                       `json:"activity_id"`
	Execution      wire.CommandExecutionReceipt `json:"execution"`
	Node           wire.CommandProgramNode      `json:"node"`
	Keys           map[string]json.RawMessage   `json:"-"`
}

func decodeReceiptRows(t *testing.T, rows []wire.CommandReceiptRow) []receiptRecord {
	t.Helper()
	out := make([]receiptRecord, len(rows))
	for i, row := range rows {
		if strings.ContainsRune(row.Text, '\n') {
			t.Fatalf("row %d spans lines: %q", row.Index, row.Text)
		}
		testutil.FailErr(t, "decode receipt record", json.Unmarshal([]byte(row.Text), &out[i]))
		testutil.FailErr(t, "decode receipt keys", json.Unmarshal([]byte(row.Text), &out[i].Keys))
	}
	return out
}

func readReceipt(t *testing.T, f programReceiptFixture, scopes []invocation.ReceiptScope, offset, limit int) wire.CommandReceiptDocumentPage {
	t.Helper()
	page, err := f.recorder.ReadReceiptDocument(t.Context(), f.session, scopes, "", offset, limit)
	testutil.FailErr(t, "read receipt document", err)
	return page
}

// repeatExecutions records count-1 further settled repetitions of one child.
func repeatExecutions(t *testing.T, f programReceiptFixture, execution string, count int) {
	t.Helper()
	_, err := f.database.ExecContext(t.Context(), `UPDATE invocation_program_executions SET state='completed',status_code=0,effect_certainty='none' WHERE id=?`, execution)
	testutil.FailErr(t, "settle representative", err)
	_, err = f.database.ExecContext(t.Context(), `WITH RECURSIVE repetitions(n) AS (SELECT 2 UNION ALL SELECT n+1 FROM repetitions WHERE n<?)
 INSERT INTO invocation_program_executions (id,program_id,node_id,ordinal,parent_execution_id,actor_id,scope_json,operation_id,definition_digest,arguments_representation,arguments_digest,arguments_ref,canonical,state,owner,lifecycle,native_command,effect_mode,effect_classes_json,started_at,status_code,effect_certainty)
 SELECT 'repeat-'||n,program_id,node_id,n,parent_execution_id,actor_id,scope_json,operation_id,definition_digest,arguments_representation,arguments_digest,arguments_ref,canonical,'completed',owner,lifecycle,native_command,effect_mode,effect_classes_json,started_at,0,'none'
 FROM invocation_program_executions,repetitions WHERE id=?`, count, execution)
	testutil.FailErr(t, "record repeated executions", err)
}

func TestReceiptDocumentOfEntryRejectedProgram(t *testing.T) {
	f := newProgramReceiptFixture(t)
	testutil.FailErr(t, "reject program at entry", f.recorder.SettleProgram(t.Context(), f.program, toolcontract.ProgramSettlement{State: "rejected", StopCode: "COMMAND_SYNTAX", DetailReference: "output:refusal"}))
	page := readReceipt(t, f, []invocation.ReceiptScope{{ProgramID: f.program}}, 0, 64)
	if page.TotalRows != 3 || !page.Settled || !page.Complete || page.EndOffset != 3 || len(page.Rows) != 3 {
		t.Fatalf("page = %+v", page)
	}
	records := decodeReceiptRows(t, page.Rows)
	header := records[0]
	if header.Type != "program" || header.Program.State != "rejected" || header.Program.StopCode != "COMMAND_SYNTAX" || header.ExecutionCount != 0 || header.NodeCount != 2 || header.Activity != nil {
		t.Fatalf("header = %+v", header)
	}
	if _, ok := header.Keys["progress"]; ok {
		t.Fatalf("program without progress record carries progress: %s", page.Rows[0].Text)
	}
	if records[1].Type != "node" || records[1].Node.ID != "cat-node" || records[2].Node.ID != "untaken-node" || records[1].ProgramID != f.program {
		t.Fatalf("node rows = %+v", records[1:])
	}
}

func TestReceiptDocumentRowOrderAndWindows(t *testing.T) {
	f := newProgramReceiptFixture(t)
	first := f.child(t)
	f.start(t, first)
	second := f.child(t)
	f.start(t, second)
	other := programReceiptFixtureOn(t, f.database, f.session, map[string]any{"command": "${op} arg"})
	for range 3 {
		other.start(t, other.child(t))
	}
	activity := first.Invocation().Identity.ExecutionID
	scopes := []invocation.ReceiptScope{{ProgramID: f.program, ActivityID: activity}, {ProgramID: other.program}}
	whole := readReceipt(t, f, scopes, 0, 500)
	// Both children of the first program share a node and operation, so one
	// activity selects both.
	if whole.TotalRows != (1+2+2)+(1+3+2) || len(whole.Rows) != whole.TotalRows || !whole.Complete || whole.Settled {
		t.Fatalf("whole document = %d rows of %d, complete %v settled %v", len(whole.Rows), whole.TotalRows, whole.Complete, whole.Settled)
	}
	records := decodeReceiptRows(t, whole.Rows)
	want := []string{"program", "execution", "execution", "node", "node", "program", "execution", "execution", "execution", "node", "node"}
	for i, record := range records {
		if record.Type != want[i] || whole.Rows[i].Index != i {
			t.Fatalf("row %d = %s (index %d), want %s", i, record.Type, whole.Rows[i].Index, want[i])
		}
	}
	if records[0].Activity == nil || records[0].Activity.ID != activity || records[0].ExecutionCount != 2 || records[0].NodeCount != 2 {
		t.Fatalf("activity header = %+v", records[0])
	}
	if records[1].ActivityID != activity || records[1].Execution.ID != activity || records[2].Execution.ID != second.Invocation().Identity.ExecutionID {
		t.Fatalf("activity executions = %+v", records[1:3])
	}
	if _, ok := records[6].Keys["activity_id"]; ok || records[6].ProgramID != other.program || records[5].Activity != nil {
		t.Fatalf("whole-program rows carry an activity: %s", whole.Rows[6].Text)
	}
	var stitched []wire.CommandReceiptRow
	for offset := 0; offset < whole.TotalRows; offset += 3 {
		page := readReceipt(t, f, scopes, offset, 3)
		if page.Revision != whole.Revision || page.Offset != offset || page.EndOffset != min(offset+3, whole.TotalRows) {
			t.Fatalf("window at %d = %+v", offset, page)
		}
		stitched = append(stitched, page.Rows...)
	}
	for i := range whole.Rows {
		if stitched[i] != whole.Rows[i] {
			t.Fatalf("window row %d differs:\n%s\n%s", i, stitched[i].Text, whole.Rows[i].Text)
		}
	}
	end := readReceipt(t, f, scopes, whole.TotalRows, 64)
	if len(end.Rows) != 0 || !end.Complete || end.EndOffset != whole.TotalRows {
		t.Fatalf("end window = %+v", end)
	}
}

func TestReceiptDocumentWindowsLargeLedgers(t *testing.T) {
	f := newProgramReceiptFixture(t)
	child := f.child(t)
	f.start(t, child)
	id := child.Invocation().Identity.ExecutionID
	repeatExecutions(t, f, id, 1200)
	quota := &outputQuota{}
	store := f.recorder.ProgramBlobStore(t.Context(), blobstore.Store{Root: t.TempDir(), Dir: tooloutput.ToolOutputSpillDir})
	lifetime, err := invocation.NewProgramOutputRetention(t.Context(), f.recorder, store, f.session, quota)
	testutil.FailErr(t, "admit output lifetime", err)
	testutil.FailErr(t, "restore empty output set", lifetime.Restore(t.Context()))
	attachStream(t, f, lifetime, store, quota, "repeat-520", "stdout", "output:repeat-stdout", "<line> & more", 64)

	for _, scope := range []invocation.ReceiptScope{{ProgramID: f.program}, {ProgramID: f.program, ActivityID: id}} {
		page := readReceipt(t, f, []invocation.ReceiptScope{scope}, 500, 64)
		if page.TotalRows != 1+1200+2 || len(page.Rows) != 64 || page.Rows[0].Index != 500 || page.Rows[63].Index != 563 {
			t.Fatalf("%s window = %d rows of %d", scope, len(page.Rows), page.TotalRows)
		}
		records := decodeReceiptRows(t, page.Rows)
		// Row r holds the execution with ordinal r.
		if records[0].Execution.ID != "repeat-500" || records[20].Execution.ID != "repeat-520" {
			t.Fatalf("%s rows start at %s", scope, records[0].Execution.ID)
		}
		outputs := records[20].Execution.Outputs
		if len(outputs) != 1 || outputs[0].Ref != "output:repeat-stdout" || outputs[0].Preview != "<line> & more" || len(records[19].Execution.Outputs) != 0 {
			t.Fatalf("%s outputs = %+v", scope, outputs)
		}
		if !strings.Contains(page.Rows[20].Text, `"preview":"<line> & more"`) {
			t.Fatalf("markup escaped in %s", page.Rows[20].Text)
		}
	}
}

func TestReceiptRevisionCoversEveryVisibleChange(t *testing.T) {
	f := newProgramReceiptFixture(t)
	scopes := []invocation.ReceiptScope{{ProgramID: f.program}}
	revision := func() string { return readReceipt(t, f, scopes, 0, 1).Revision }
	current := revision()
	if again := revision(); again != current {
		t.Fatal("revision changed without a write")
	}
	advance := func(label string, write func()) {
		t.Helper()
		write()
		next := revision()
		if next == current {
			t.Fatalf("%s left the revision unchanged", label)
		}
		if _, err := f.recorder.ReadReceiptDocument(t.Context(), f.session, scopes, current, 0, 1); !errors.Is(err, invocation.ErrReceiptChanged) {
			t.Fatalf("%s: pinned read err = %v", label, err)
		}
		current = next
	}
	child := f.child(t)
	advance("child start", func() { f.start(t, child) })
	advance("child settlement", func() {
		result := toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.NoEffect}
		testutil.FailErr(t, "settle child", f.recorder.SettleChild(t.Context(), child.Invocation().Identity.ExecutionID, toolcontract.ProgramChildSettlement{Result: result, OwnerInvoked: true, DetailReference: "output:done"}))
	})
	advance("output attach", func() {
		quota := &outputQuota{}
		store := f.recorder.ProgramBlobStore(t.Context(), blobstore.Store{Root: t.TempDir(), Dir: tooloutput.ToolOutputSpillDir})
		lifetime, err := invocation.NewProgramOutputRetention(t.Context(), f.recorder, store, f.session, quota)
		testutil.FailErr(t, "admit output lifetime", err)
		testutil.FailErr(t, "restore outputs", lifetime.Restore(t.Context()))
		attachStream(t, f, lifetime, store, quota, child.Invocation().Identity.ExecutionID, "stdout", "output:stdout", "body", 16)
	})
	advance("node marked untaken", func() {
		testutil.FailErr(t, "mark untaken", f.recorder.MarkUntaken(t.Context(), f.program, "untaken-node"))
	})
	advance("progress refusal", func() {
		testutil.FailErr(t, "refuse progress", f.recorder.RecordProgressRefusal(t.Context(), f.program, "output:progress-refused"))
	})
	advance("restart interruption", func() {
		_, err := f.recorder.InterruptPrograms(t.Context(), f.session, "output:host-loss")
		testutil.FailErr(t, "interrupt programs", err)
	})
	if page := readReceipt(t, f, scopes, 0, 1); !page.Settled {
		t.Fatal("interrupted program is not settled")
	}
}

func TestReceiptSettlementChangesRevisionAndSettled(t *testing.T) {
	f := newProgramReceiptFixture(t)
	scopes := []invocation.ReceiptScope{{ProgramID: f.program}}
	before := readReceipt(t, f, scopes, 0, 1)
	if before.Settled {
		t.Fatal("running program is settled")
	}
	testutil.FailErr(t, "settle program", f.recorder.SettleProgram(t.Context(), f.program, toolcontract.ProgramSettlement{State: "completed", StopCode: "", DetailReference: "output:program"}))
	after := readReceipt(t, f, scopes, 0, 1)
	if !after.Settled || after.Revision == before.Revision {
		t.Fatalf("settlement: settled %v, revision unchanged %v", after.Settled, after.Revision == before.Revision)
	}
}

func TestReceiptDocumentRefusesUnknownScopesAndOffsets(t *testing.T) {
	f := newProgramReceiptFixture(t)
	read := func(session string, scope invocation.ReceiptScope, offset int) error {
		_, err := f.recorder.ReadReceiptDocument(t.Context(), session, []invocation.ReceiptScope{scope}, "", offset, 64)
		return err
	}
	if err := read("another-session", invocation.ReceiptScope{ProgramID: f.program}, 0); !errors.Is(err, invocation.ErrProgramNotFound) {
		t.Fatalf("foreign session err = %v", err)
	}
	if err := read(f.session, invocation.ReceiptScope{ProgramID: uuid.NewString()}, 0); !errors.Is(err, invocation.ErrProgramNotFound) {
		t.Fatalf("missing program err = %v", err)
	}
	if err := read(f.session, invocation.ReceiptScope{ProgramID: f.program, ActivityID: uuid.NewString()}, 0); !errors.Is(err, invocation.ErrProgramNotFound) {
		t.Fatalf("unknown activity err = %v", err)
	}
	var request *invocation.ReceiptRequestError
	if err := read(f.session, invocation.ReceiptScope{ProgramID: f.program}, 4); !errors.As(err, &request) || request.Parameter != "offset" {
		t.Fatalf("offset past end err = %v", err)
	}
}

func TestParseReceiptScopes(t *testing.T) {
	program, activity, other := uuid.NewString(), uuid.NewString(), uuid.NewString()
	scopes, err := invocation.ParseReceiptScopes([]string{program + ":" + activity, strings.ToUpper(other)})
	testutil.FailErr(t, "parse scopes", err)
	if len(scopes) != 2 || scopes[0] != (invocation.ReceiptScope{ProgramID: program, ActivityID: activity}) || scopes[1].ProgramID != other || scopes[1].String() != other {
		t.Fatalf("scopes = %+v", scopes)
	}
	tooMany := make([]string, invocation.MaxReceiptScopes+1)
	for i := range tooMany {
		tooMany[i] = uuid.NewString()
	}
	for name, values := range map[string][]string{
		"none":               nil,
		"too many":           tooMany,
		"malformed program":  {"not-a-program"},
		"malformed activity": {program + ":activity"},
		"repeated":           {program, program},
		"whole and activity": {program, program + ":" + activity},
		"activity and whole": {program + ":" + activity, program},
	} {
		var request *invocation.ReceiptRequestError
		if _, err := invocation.ParseReceiptScopes(values); !errors.As(err, &request) || request.Parameter != "scopes" {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	if _, err := invocation.ParseReceiptScopes([]string{program + ":" + activity, program + ":" + other}); err != nil {
		t.Fatalf("two activities of one program: %v", err)
	}
}

func TestReceiptSearchFindsRowsAndPages(t *testing.T) {
	f := newProgramReceiptFixture(t)
	child := f.child(t)
	f.start(t, child)
	scopes := []invocation.ReceiptScope{{ProgramID: f.program}}
	page := readReceipt(t, f, scopes, 0, 64)
	search := func(query, cursor string, sensitive bool, limit int) wire.CommandReceiptSearchPage {
		t.Helper()
		result, err := f.recorder.SearchReceiptDocument(t.Context(), f.session, scopes, invocation.ReceiptSearch{Revision: page.Revision, Query: query, Cursor: cursor, CaseSensitive: sensitive, Limit: limit})
		testutil.FailErr(t, "search receipt", err)
		return result
	}
	all := search(`"TYPE"`, "", false, 50)
	if len(all.Matches) != page.TotalRows || all.NextCursor != "" {
		t.Fatalf("insensitive type matches = %+v", all)
	}
	for i, match := range all.Matches {
		text := []rune(page.Rows[match.Row].Text)
		if match.Row != i || strings.ToLower(string(text[match.Column:match.Column+match.Length])) != `"type"` {
			t.Fatalf("match %d = %+v", i, match)
		}
	}
	if sensitive := search(`"TYPE"`, "", true, 50); len(sensitive.Matches) != 0 {
		t.Fatalf("sensitive matches = %+v", sensitive.Matches)
	}
	first := search(`"type"`, "", true, 2)
	if len(first.Matches) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %+v", first)
	}
	rest := search(`"type"`, first.NextCursor, true, 50)
	if len(rest.Matches) != page.TotalRows-2 || rest.Matches[0].Row != 2 || rest.NextCursor != "" {
		t.Fatalf("second page = %+v", rest)
	}
	if _, err := f.recorder.SearchReceiptDocument(t.Context(), f.session, scopes, invocation.ReceiptSearch{Revision: page.Revision, Query: `"type"`, Cursor: first.NextCursor + "x", CaseSensitive: true, Limit: 2}); !errors.Is(err, pagecursor.ErrInvalid) {
		t.Fatalf("tampered cursor err = %v", err)
	}
	if _, err := f.recorder.SearchReceiptDocument(t.Context(), f.session, scopes, invocation.ReceiptSearch{Revision: page.Revision, Query: `"type"`, Cursor: first.NextCursor, CaseSensitive: false, Limit: 2}); !errors.Is(err, pagecursor.ErrInvalid) {
		t.Fatalf("cursor reused with other options err = %v", err)
	}
	f.start(t, f.child(t))
	if _, err := f.recorder.SearchReceiptDocument(t.Context(), f.session, scopes, invocation.ReceiptSearch{Revision: page.Revision, Query: "x", Limit: 2}); !errors.Is(err, invocation.ErrReceiptChanged) {
		t.Fatalf("stale revision err = %v", err)
	}
}

func TestReceiptSearchBoundsRowsPerRequest(t *testing.T) {
	f := newProgramReceiptFixture(t)
	child := f.child(t)
	f.start(t, child)
	repeatExecutions(t, f, child.Invocation().Identity.ExecutionID, 9000)
	scopes := []invocation.ReceiptScope{{ProgramID: f.program}}
	page := readReceipt(t, f, scopes, 0, 1)
	var cursor string
	requests := 0
	for {
		result, err := f.recorder.SearchReceiptDocument(t.Context(), f.session, scopes, invocation.ReceiptSearch{Revision: page.Revision, Query: "no such text", Cursor: cursor, Limit: 50})
		testutil.FailErr(t, "search without matches", err)
		requests++
		if len(result.Matches) != 0 {
			t.Fatalf("matches = %+v", result.Matches)
		}
		if result.NextCursor == "" {
			break
		}
		cursor = result.NextCursor
	}
	// 9,003 rows at 8,192 rows per request.
	if requests != 2 {
		t.Fatalf("requests = %d", requests)
	}
	tail, err := f.recorder.SearchReceiptDocument(t.Context(), f.session, scopes, invocation.ReceiptSearch{Revision: page.Revision, Query: `"untaken-node"`, Cursor: cursor, CaseSensitive: true, Limit: 50})
	testutil.FailErr(t, "search tail", err)
	if len(tail.Matches) != 1 || tail.Matches[0].Row != page.TotalRows-1 {
		t.Fatalf("tail matches = %+v", tail.Matches)
	}
}
