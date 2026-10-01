package db

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

const gcGuardSha = "aa11bb22cc33dd44ee55ff6677889900aa11bb22cc33dd44ee55ff6677889900"

func seedContentBlobObject(t *testing.T, database Handle, projectID, sha string) {
	t.Helper()
	_, err := database.ExecContext(t.Context(), `
INSERT INTO content_blob_objects (project_id, sha256, byte_size, stored_size, created_at)
VALUES (?, ?, 10, 8, '2026-08-26T00:00:00Z')`, projectID, sha)
	testutil.FailErr(t, "insert content blob object", err)
}

func seedEvidenceRecordCiting(t *testing.T, database Handle, sessionID, projectID, sha string) {
	t.Helper()
	_, err := database.ExecContext(t.Context(), `
INSERT INTO evidence_records (session_id, project_id, handle, ordinal, kind, content_blob_sha256)
VALUES (?, ?, 'read#1', 1, 'read', ?)`, sessionID, projectID, sha)
	testutil.FailErr(t, "insert evidence record", err)
}

func contentBlobObjectCount(t *testing.T, database Handle, projectID, sha string) int {
	t.Helper()
	var n int
	err := database.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM content_blob_objects WHERE project_id = ? AND sha256 = ?`,
		projectID, sha).Scan(&n)
	testutil.FailErr(t, "count content blob objects", err)
	return n
}

// A row re-referenced after the batch listing must survive the delete; the
// statement re-checks reachability itself.
func TestDeleteUnreferencedContentBlobObjectKeepsReReferencedBody(t *testing.T) {
	database := openTestDB(t)
	queries := New(database)
	testdbseed.InsertProject(t, database, testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, database, "sess-1", testdbseed.DefaultProjectID)
	seedContentBlobObject(t, database, testdbseed.DefaultProjectID, gcGuardSha)
	// The re-reference that landed after the candidate was listed.
	seedEvidenceRecordCiting(t, database, "sess-1", testdbseed.DefaultProjectID, gcGuardSha)

	deleted, err := queries.DeleteUnreferencedContentBlobObject(t.Context(), DeleteUnreferencedContentBlobObjectParams{
		ProjectID: testdbseed.DefaultProjectID, Sha256: gcGuardSha,
	})
	testutil.FailErr(t, "DeleteUnreferencedContentBlobObject", err)
	if deleted != 0 {
		t.Fatalf("deleted = %d want 0: a re-referenced body must survive the sweep", deleted)
	}
	if got := contentBlobObjectCount(t, database, testdbseed.DefaultProjectID, gcGuardSha); got != 1 {
		t.Fatalf("object rows = %d want 1", got)
	}
}

// A genuinely unreferenced body is still reclaimed.
func TestDeleteUnreferencedContentBlobObjectDeletesUnreferencedBody(t *testing.T) {
	database := openTestDB(t)
	queries := New(database)
	testdbseed.InsertProject(t, database, testdbseed.DefaultProjectID)
	seedContentBlobObject(t, database, testdbseed.DefaultProjectID, gcGuardSha)

	deleted, err := queries.DeleteUnreferencedContentBlobObject(t.Context(), DeleteUnreferencedContentBlobObjectParams{
		ProjectID: testdbseed.DefaultProjectID, Sha256: gcGuardSha,
	})
	testutil.FailErr(t, "DeleteUnreferencedContentBlobObject", err)
	if deleted != 1 {
		t.Fatalf("deleted = %d want 1 for an unreferenced body", deleted)
	}
	if got := contentBlobObjectCount(t, database, testdbseed.DefaultProjectID, gcGuardSha); got != 0 {
		t.Fatalf("object rows = %d want 0", got)
	}
}

// Reachability is project-scoped: another project's reference to the same
// digest does not retain this project's body.
func TestDeleteUnreferencedContentBlobObjectIsProjectScoped(t *testing.T) {
	database := openTestDB(t)
	queries := New(database)
	const otherProject = "11111111-2222-4333-8444-555555555555"
	testdbseed.InsertProject(t, database, testdbseed.DefaultProjectID)
	testdbseed.InsertProject(t, database, otherProject)
	testdbseed.InsertSession(t, database, "sess-other", otherProject)
	seedContentBlobObject(t, database, testdbseed.DefaultProjectID, gcGuardSha)
	seedEvidenceRecordCiting(t, database, "sess-other", otherProject, gcGuardSha)

	deleted, err := queries.DeleteUnreferencedContentBlobObject(t.Context(), DeleteUnreferencedContentBlobObjectParams{
		ProjectID: testdbseed.DefaultProjectID, Sha256: gcGuardSha,
	})
	testutil.FailErr(t, "DeleteUnreferencedContentBlobObject", err)
	if deleted != 1 {
		t.Fatalf("deleted = %d want 1: another project's reference must not retain this one", deleted)
	}
}
