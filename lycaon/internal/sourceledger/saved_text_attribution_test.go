package sourceledger

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// A line attributed from saved text names the change that published it, as
// the ledger path does, so line facts can ask about that change.
func TestSavedTextAttributionNamesThePublishingEffect(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProject(t, sqlDB, "p1")
	_, err := sqlDB.ExecContext(t.Context(), `INSERT INTO project_roots (id, project_id, path, label, is_primary, added_at, kind)
		VALUES ('r1', 'p1', '/tmp/source-ledger-test', 'source-ledger-test', 1, ?, 'attached')`, db.FormatTime(time.Now().UTC()))
	testutil.FailErr(t, "insert root", err)
	store, ctx := New(sqlDB, t.TempDir()), t.Context()
	location := RecordLocation{RootID: "r1", Path: "a.txt"}
	base := &TextState{DocumentID: "doc", Epoch: 1, Revision: 1, Spans: []TextSpan{{Index: 0, Length: 6, Client: 1}}}
	mustRecord(t, store, ctx, RecordInput{RecordLocation: location, ProjectID: "p1", Op: api.SourceChangeOpCreate,
		Origin: api.SourceChangeOriginExternal, OperationID: "op-create", After: []byte("alpha\n"), TextAfter: base})
	created, err := store.History.QueryAttribution(ctx, "p1", sourcebranch.Trunk, "r1", "a.txt")
	testutil.FailErr(t, "created", err)

	tx, err := sqlDB.BeginTx(ctx, nil)
	testutil.FailErr(t, "begin", err)
	testutil.FailErr(t, "contribution", RecordTextContributionTx(ctx, tx, TextContribution{ProjectID: "p1", FileID: created.FileID,
		DocumentID: "doc", Epoch: 1, Revision: 2, OperationID: "op-edit", Origin: api.SourceChangeOriginAgent, SessionID: "s1", Turn: 1,
		ToolCallID: "call-1", ToolName: "edit", Inserted: []TextIdentityRange{{Client: 2, End: 6}}, CreatedAt: time.Now().UTC()}))
	testutil.FailErr(t, "commit", tx.Commit())
	edited := &TextState{DocumentID: "doc", Epoch: 1, Revision: 2, Spans: []TextSpan{{Index: 0, Length: 6, Client: 1}, {Index: 6, Length: 6, Client: 2}}}
	mustRecord(t, store, ctx, RecordInput{RecordLocation: location, ProjectID: "p1", Op: api.SourceChangeOpWrite,
		Origin: api.SourceChangeOriginAgent, SessionID: "s1", Turn: 1, ToolCallID: "call-1", OperationID: "op-edit",
		Before: []byte("alpha\n"), After: []byte("alpha\ngamma\n"), TextBefore: base, TextAfter: edited})

	res, err := store.History.QueryAttribution(ctx, "p1", sourcebranch.Trunk, "r1", "a.txt")
	testutil.FailErr(t, "attribution", err)
	var effectID string
	for _, interval := range res.Intervals {
		if interval.Origin == api.SourceChangeOriginAgent && interval.StartLine == 2 {
			effectID = interval.EffectID
		}
	}
	if effectID == "" {
		t.Fatalf("the agent's line names no change: %+v", res.Intervals)
	}
	var operation string
	testutil.FailErr(t, "effect", sqlDB.QueryRowContext(ctx, `SELECT operation_id FROM source_effects WHERE id = ?`, effectID).Scan(&operation))
	if operation != "op-edit" {
		t.Fatalf("line names effect of %s, want the edit", operation)
	}
}
