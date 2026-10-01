package sourceledger

import (
	"database/sql"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestUserOperationsNameTheActingPersonAndOthersNameNone(t *testing.T) {
	store, ctx := openLedger(t)
	owner, err := store.people.HostOwner(ctx)
	testutil.FailErr(t, "read host owner", err)

	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "person.txt", OperationID: "by-person",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser, After: []byte("typed\n"),
	})
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "agent.txt", OperationID: "by-agent",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent, After: []byte("generated\n"),
		SessionID: "", ToolCallID: "call-1", ToolName: "write",
	})
	for key, want := range map[string]string{"by-person": owner.ID, "by-agent": ""} {
		var person sql.NullString
		testutil.FailErr(t, "read operation "+key, store.sqlDB.QueryRowContext(ctx,
			`SELECT person_id FROM source_operations WHERE project_id = 'p1' AND operation_key = ?`, key).Scan(&person))
		if person.String != want {
			t.Fatalf("%s person = %q, want %q", key, person.String, want)
		}
	}
}
