package storetest_test

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFixturesPersistSessionsInIsolatedDatabases(t *testing.T) {
	first := storetest.New(t, "project")
	second := storetest.New(t, "project")
	session, err := first.Sessions.Create(t.Context(), api.CreateSessionRequest{}, "project")
	testutil.FailErr(t, "create session", err)
	if session.WorkspaceRootID == "" || session.WorkspacePath == "" {
		t.Fatal("fixture has no attached workspace")
	}
	var rootID string
	err = first.DB().QueryRowContext(t.Context(), `SELECT workspace_root_id FROM sessions WHERE id = ?`, session.ID).Scan(&rootID)
	testutil.FailErr(t, "read persisted session", err)
	if rootID != session.WorkspaceRootID {
		t.Fatal("workspace binding was not persisted")
	}

	if _, err := second.Sessions.Get(t.Context(), session.ID); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("other fixture read session: %v", err)
	}
}

func TestEmptyFixtureLeavesProjectCreationToTheCaller(t *testing.T) {
	sessions := storetest.New(t)
	var count int
	err := sessions.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM projects`).Scan(&count)
	testutil.FailErr(t, "count projects", err)
	if count != 0 {
		t.Fatalf("fixture created %d unrequested projects", count)
	}
}
