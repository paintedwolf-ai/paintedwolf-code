package eventing

import (
	"context"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

type countingProjectLookup struct {
	calls int
}

func (l *countingProjectLookup) ResolveProject(context.Context, string) (string, string, bool, error) {
	l.calls++
	return "looked-up-project", "", true, nil
}

func TestSourceChangedTxDoesNotReenterProjectLookup(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	lookup := &countingProjectLookup{}
	feed := outboxSourceFeed{outbox: eventoutbox.New(sqlDB, nil), lookup: lookup}
	tx, err := sqlDB.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin transaction", err)
	testutil.FailErr(t, "enqueue source change", feed.SourceChangedTx(
		t.Context(),
		tx,
		api.SourceChangesEvent{
			ProjectID: " 54f6d2ef-89c3-56e9-9a76-f6b1a2a54476 ", WorkspaceID: "workspace-1",
			WorkspaceKind: api.SourceWorkspaceKindProject,
			Changes:       []api.SourceChange{{RootID: "root-1", Path: "src/main.go"}},
		},
	))
	testutil.FailErr(t, "commit transaction", tx.Commit())

	if lookup.calls != 0 {
		t.Fatalf("project lookup calls = %d, want 0 inside transaction", lookup.calls)
	}
	var projectID, sessionID, facet string
	err = sqlDB.QueryRowContext(t.Context(), `
		SELECT project_id, session_id, facet
		FROM event_outbox
	`).Scan(&projectID, &sessionID, &facet)
	testutil.FailErr(t, "read queued event", err)
	if projectID != "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476" || sessionID != "" || facet != "project\x00workspace-1\x00root-1\x00src/main.go" {
		t.Fatalf("queued scope = (%q, %q, %q)", projectID, sessionID, facet)
	}
}

func TestSourceChangeFacetsSeparateWorkspaceKinds(t *testing.T) {
	event := api.SourceChangesEvent{
		WorkspaceID: "workspace-1",
		Changes:     []api.SourceChange{{RootID: "root-1", Path: "src/main.go"}},
	}
	event.WorkspaceKind = api.SourceWorkspaceKindProject
	projectFacet := sourceChangesFacet(event)
	event.WorkspaceKind = api.SourceWorkspaceKindWorker
	if workerFacet := sourceChangesFacet(event); workerFacet == projectFacet {
		t.Fatalf("workspace facets collided: %q", workerFacet)
	}
}
