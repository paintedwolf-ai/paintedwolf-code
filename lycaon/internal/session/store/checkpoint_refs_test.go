package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCheckpointBodiesShareContentUntilLastOwnerReleases(t *testing.T) {
	root := t.TempDir()
	database := testdbfixture.OpenPath(t, filepath.Join(root, "store.db"))
	testdbseed.InsertSession(t, database, "first", testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, database, "second", testdbseed.DefaultProjectID)
	body := []byte("one source body used by independent rewind anchors")
	sha := sourceblob.ContentSHA(body)
	objects := sourceblob.New(filepath.Join(root, "source-content"))
	rel, stored, oids, err := objects.Put(sha, body)
	testutil.FailErr(t, "publish compressed body", err)
	ref := CheckpointObject{Path: "file.go", SHA256: sha, Rel: rel, GitSHA1: oids.SHA1, GitSHA256: oids.SHA256, Size: int64(len(body)), StoredSize: stored, Mode: 0o644}
	s := NewSQL(database)
	for _, id := range []string{"first", "second"} {
		testutil.FailErr(t, "publish checkpoint "+id, s.PutCheckpoint(t.Context(), "root", id, "anchor", "2020-01-01T00:00:00Z", "{}", []CheckpointObject{ref}))
	}
	var count int
	testutil.FailErr(t, "count deduplicated objects", database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM source_blob_objects`).Scan(&count))
	if count != 1 {
		t.Fatalf("object count=%d want1", count)
	}
	testutil.FailErr(t, "drop first owner", s.DropCheckpoint(t.Context(), "first", "anchor"))
	candidates, err := db.New(database).ListSourceBlobReclaimCandidates(t.Context(), 10)
	testutil.FailErr(t, "check shared ownership", err)
	if len(candidates) != 1 || candidates[0].Referenced == 0 {
		t.Fatal("remaining checkpoint did not protect shared body")
	}
	testutil.FailErr(t, "drop last owner", s.DropCheckpoint(t.Context(), "second", "anchor"))
	candidates, err = db.New(database).ListSourceBlobReclaimCandidates(t.Context(), 10)
	testutil.FailErr(t, "check last ownership release", err)
	if len(candidates) != 1 || candidates[0].Referenced != 0 {
		t.Fatal("last release did not make body reclaimable")
	}
}

func TestCheckpointRootBindingAndScopedDetach(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "session", testdbseed.DefaultProjectID)
	testdbseed.InsertProject(t, database, "other-project")
	testdbseed.InsertSession(t, database, "other-session", "other-project")
	s := NewSQL(database)
	for _, sessionID := range []string{"session", "other-session"} {
		testutil.FailErr(t, "publish root-bound checkpoint", s.PutCheckpoint(t.Context(), "first-root", sessionID, "anchor", "2020-01-01T00:00:00Z", "{}", nil))
	}
	if _, err := s.ReadCheckpoint(t.Context(), "second-root", "session", "anchor"); !errors.Is(err, ErrCheckpointMissing) {
		t.Fatalf("another workspace could read checkpoint: %v", err)
	}
	if err := s.PutCheckpoint(t.Context(), "second-root", "session", "anchor", "2020-01-01T00:00:00Z", "{}", nil); err == nil {
		t.Fatal("another workspace could replace checkpoint")
	}
	testutil.FailErr(t, "detach one project's root", s.DropRootCheckpoints(t.Context(), testdbseed.DefaultProjectID, "first-root"))
	if _, err := s.ReadCheckpoint(t.Context(), "first-root", "session", "anchor"); !errors.Is(err, ErrCheckpointMissing) {
		t.Fatalf("detached root retained checkpoint: %v", err)
	}
	_, err := s.ReadCheckpoint(t.Context(), "first-root", "other-session", "anchor")
	testutil.FailErr(t, "retain another project's checkpoint at the same path", err)
}

func TestCheckpointCannotPublishForMissingOrPrunedOwner(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	s := NewSQL(database)
	if err := s.PutCheckpoint(t.Context(), "root", "missing", "anchor", "2020-01-01T00:00:00Z", "{}", nil); err == nil {
		t.Fatal("missing session accepted checkpoint")
	}
	testdbseed.InsertSession(t, database, "session", testdbseed.DefaultProjectID)
	testutil.FailErr(t, "publish empty checkpoint", s.PutCheckpoint(t.Context(), "root", "session", "anchor", "2020-01-01T00:00:00Z", "{}", nil))
	_, err := database.ExecContext(t.Context(), `UPDATE checkpoint_anchors SET pruned_at='2020-01-02T00:00:00Z' WHERE session_id='session'`)
	testutil.FailErr(t, "mark checkpoint pruned", err)
	if err := s.PutCheckpoint(t.Context(), "root", "session", "anchor", "2020-01-01T00:00:00Z", "{}", nil); err == nil {
		t.Fatal("pruned checkpoint revived")
	}
}
