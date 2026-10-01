package store

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMaintenanceScopesPageLiveIDsAndRetainArchivedOwners(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	root := t.TempDir()
	rootID := testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, root)
	for i := 0; i < 270; i++ {
		id := fmt.Sprintf("session-%03d", i)
		testdbseed.InsertSession(t, database, id, testdbseed.DefaultProjectID)
		_, err := database.ExecContext(t.Context(), `UPDATE sessions SET workspace_root_id=? WHERE id=?`, rootID, id)
		testutil.FailErr(t, "bind session root", err)
	}
	_, err := database.ExecContext(t.Context(), `UPDATE sessions SET archived_at='2020-01-01T00:00:00Z' WHERE id='session-000'`)
	testutil.FailErr(t, "archive session", err)
	s := NewSQL(database)
	var after string
	count := 0
	for {
		ids, err := s.WorkspaceNotificationIDs(t.Context(), root, after)
		testutil.FailErr(t, "page notification owners", err)
		if len(ids) == 0 {
			break
		}
		if len(ids) > maintenanceSessionBatch {
			t.Fatal("unbounded notification page")
		}
		count += len(ids)
		after = ids[len(ids)-1]
	}
	if count != 269 {
		t.Fatalf("notification count=%d want269", count)
	}
	exists, err := s.ExistingSessionIDs(t.Context(), []string{"session-000", "session-001", "gone"})
	testutil.FailErr(t, "query checkpoint ownership", err)
	if !exists["session-000"] || !exists["session-001"] || exists["gone"] {
		t.Fatalf("incorrect owners: %+v", exists)
	}
}
