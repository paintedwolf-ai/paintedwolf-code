package db

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProjectRemovalRetentionKeepsUnsettledReceipts(t *testing.T) {
	database := openTestDB(t)
	old := FormatTime(time.Now().UTC().Add(-40 * 24 * time.Hour))
	fresh := FormatTime(time.Now().UTC())
	for _, row := range []struct {
		id      string
		settled bool
		at      string
	}{{"old", true, old}, {"fresh", true, fresh}, {"interrupted", false, old}} {
		_, err := database.ExecContext(t.Context(), `INSERT INTO project_removals(operation_id,project_id,request_json,result_json,settled,created_at) VALUES(?,'deleted-project','{}','{}',?,?)`, row.id, row.settled, row.at)
		testutil.FailErr(t, "seed removal receipt", err)
	}
	_, err := purgeOperationJournals(t.Context(), database, time.Now().UTC(), DefaultOperationJournalRetention, 500)
	testutil.FailErr(t, "expire removal receipts", err)
	for id, want := range map[string]bool{"old": false, "fresh": true, "interrupted": true} {
		if got := rowExists(t, database, `SELECT 1 FROM project_removals WHERE operation_id=?`, id); got != want {
			t.Fatalf("receipt %s retained=%v want=%v", id, got, want)
		}
	}
}
