package db

import (
	"database/sql"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCommandWindowCoverageFollowsItsWindow(t *testing.T) {
	path := copyReleasedStore(t, "1.0.2")
	testutil.FailErr(t, "upgrade released source", UpgradeStaged(t.Context(), path))
	store, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open upgraded store", err)
	defer store.Close()
	store.SetMaxOpenConns(1)
	_, err = store.Exec(`PRAGMA foreign_keys = ON`)
	testutil.FailErr(t, "enforce foreign keys", err)
	_, err = store.Exec(`INSERT INTO projects(id,last_opened_at,created_at) VALUES('p6','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	testutil.FailErr(t, "create project", err)
	_, err = store.Exec(`INSERT INTO source_command_windows(id,project_id,session_id,tool_name,command_line,state,ordinal,started_ts) VALUES('w6','p6','s6','command','make','ended',1,'2026-01-01T00:00:00Z')`)
	testutil.FailErr(t, "create window", err)
	_, err = store.Exec(`INSERT INTO source_command_window_coverage(window_id,root_id,path,coverage,reason) VALUES('w6','outside','home/me','not_observed','time_budget')`)
	testutil.FailErr(t, "record a gap", err)
	if _, err := store.Exec(`INSERT INTO source_command_window_coverage(window_id,root_id,path,coverage) VALUES('w6','outside','srv','not_observed')`); err == nil {
		t.Fatal("a gap without a reason was accepted")
	}
	_, err = store.Exec(`DELETE FROM source_command_windows WHERE id='w6'`)
	testutil.FailErr(t, "delete window", err)
	var left int
	testutil.FailErr(t, "count coverage", store.QueryRow(`SELECT count(*) FROM source_command_window_coverage`).Scan(&left))
	if left != 0 {
		t.Fatalf("%d coverage rows outlived their window", left)
	}
}
