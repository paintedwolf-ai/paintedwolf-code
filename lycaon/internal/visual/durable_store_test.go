package visual

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

const artifactsSubdir = "artifacts"

type durableFixture struct {
	store   *DurableStore
	sqlDB   db.Handle
	queries *db.Queries
	dataDir string
	project string
}

func newDurableFixture(t *testing.T, roots []string) durableFixture {
	t.Helper()
	projectID := uuid.NewString()
	dataDir := t.TempDir()
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProject(t, sqlDB, projectID)
	for _, root := range roots {
		testdbseed.InsertSession(t, sqlDB, root, projectID)
	}
	store := NewDurableStore(DurableConfig{
		DataDir: dataDir,
		ArtifactsDir: func(id string) (string, error) {
			dir := filepath.Join(dataDir, "projects", id, artifactsSubdir)
			if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
				return "", mkErr
			}
			return dir, nil
		},
		Lookup:  func(context.Context, string) (string, error) { return projectID, nil },
		Records: NewRecords(sqlDB, eventoutbox.New(sqlDB, nil), testProjection()),
	})
	return durableFixture{store: store, sqlDB: sqlDB, queries: db.New(sqlDB), dataDir: dataDir, project: projectID}
}

func (f durableFixture) blobDir(t *testing.T) string {
	t.Helper()
	dir, err := f.store.artifactsDir(f.project)
	testutil.FailErr(t, "artifactsDir failed", err)
	return dir
}

func (f durableFixture) record(t *testing.T, artifactID string) ArtifactRecord {
	t.Helper()
	rec, found, err := f.store.records.GetInProject(t.Context(), f.project, artifactID)
	testutil.FailErr(t, "records.GetInProject failed", err)
	if !found {
		t.Fatalf("no artifacts row for %s", artifactID)
	}
	return rec
}

func (f durableFixture) blobExists(t *testing.T, rec ArtifactRecord) bool {
	t.Helper()
	_, ok := readArtifactBlob(f.blobDir(t), rec.ContentHash, rec.ByteSize)
	return ok
}

func (f durableFixture) restart() *DurableStore {
	return NewDurableStore(DurableConfig{
		DataDir:      f.dataDir,
		ArtifactsDir: f.store.artifactsDir,
		Lookup:       func(context.Context, string) (string, error) { return f.project, nil },
		Records:      NewRecords(f.sqlDB, eventoutbox.New(f.sqlDB, nil), testProjection()),
	})
}

// testProjection uses the production transactional write path.
func testProjection() ArtifactProjection {
	return ArtifactProjection{
		Write: func(ctx context.Context, tx *sql.Tx, projectID string, rec ArtifactRecord) error {
			return search.ProjectArtifactTx(ctx, tx, projectID, search.ProjectArtifactInput{
				ID:             rec.ID,
				Hash:           rec.ContentHash,
				Mime:           rec.Mime,
				Source:         rec.Source,
				Caption:        rec.Caption,
				EvidenceHandle: rec.EvidenceHandle,
				SessionID:      rec.SessionID,
				WorkflowRunID:  rec.WorkflowRunID,
				ToolCallID:     rec.ToolCallID,
				CreatedAt:      rec.CreatedAt,
			})
		},
		Delete: search.DeleteArtifactProjectionTx,
	}
}

func onePixelPNG(t *testing.T) []byte {
	t.Helper()
	return TestPNG1x1Bytes()
}

func TestDurableStorePutWritesRecordProjectionAndBlob(t *testing.T) {
	f := newDurableFixture(t, []string{"root-1"})
	wire, err := f.store.Put(t.Context(), "root-1", Entry{
		Meta: api.VisualArtifact{
			Mime:    "image/png",
			Source:  api.VisualArtifactSourceRender,
			Caption: "Signup — empty state",
		},
		Bytes: onePixelPNG(t),
	})
	testutil.FailErr(t, "Put failed", err)

	rec := f.record(t, wire.ID)
	if rec.ContentHash != artifactContentHash(onePixelPNG(t)) {
		t.Fatalf("content_hash = %q", rec.ContentHash)
	}
	if rec.ByteSize != int64(len(onePixelPNG(t))) {
		t.Fatalf("byte_size = %d want %d", rec.ByteSize, len(onePixelPNG(t)))
	}
	AssertArtifactRecord(t, rec)

	// The projection commits with the row, not after it.
	var projected int
	testutil.FailErr(t, "count evidence rows", f.sqlDB.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM evidence_index WHERE source_ref = ? AND hit_kind = 'artifact'`, wire.ID).Scan(&projected))
	if projected != 1 {
		t.Fatalf("evidence_index rows = %d want 1", projected)
	}

	if _, err := os.Stat(filepath.Join(f.blobDir(t), rec.ContentHash)); err != nil {
		t.Fatalf("stat artifact blob: %v", err)
	}
}

func TestDurableStorePutRemovesBlobWhenCommitFails(t *testing.T) {
	f := newDurableFixture(t, []string{"root-1"})
	boom := errors.New("projection write failed")
	f.store.records = NewRecords(f.sqlDB, eventoutbox.New(f.sqlDB, nil), ArtifactProjection{
		Write: func(context.Context, *sql.Tx, string, ArtifactRecord) error { return boom },
	})
	body := append(onePixelPNG(t), "uncommitted artifact"...)
	_, err := f.store.Put(t.Context(), "root-1", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender},
		Bytes: body,
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Put error = %v want projection failure", err)
	}
	if _, ok := readArtifactBlob(f.blobDir(t), artifactContentHash(body), int64(len(body))); ok {
		t.Fatal("uncommitted artifact bytes survived")
	}
}

func TestDurableStoreRePutReplacesInsteadOfAccumulating(t *testing.T) {
	f := newDurableFixture(t, []string{"root-1"})
	operation := uuid.NewString()
	frameOne := append(onePixelPNG(t), "frame-one"...)
	frameTwo := append(onePixelPNG(t), "frame-two"...)
	first, err := f.store.Put(t.Context(), "root-1", Entry{
		Meta:        api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture, Caption: "take 1"},
		OperationID: operation,
		Bytes:       frameOne,
	})
	testutil.FailErr(t, "first Put failed", err)
	second, err := f.store.Put(t.Context(), "root-1", Entry{
		Meta:        api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture, Caption: "take 2"},
		OperationID: operation,
		Bytes:       frameTwo,
	})
	testutil.FailErr(t, "second Put failed", err)
	if first.ID != second.ID {
		t.Fatalf("operation %s minted two ids: %s vs %s", operation, first.ID, second.ID)
	}

	var artifacts, evidence int
	testutil.FailErr(t, "count artifacts", f.sqlDB.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM artifacts WHERE project_id = ?`, f.project).Scan(&artifacts))
	testutil.FailErr(t, "count evidence", f.sqlDB.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM evidence_index WHERE hit_kind = 'artifact'`).Scan(&evidence))
	if artifacts != 1 || evidence != 1 {
		t.Fatalf("rows after re-put: artifacts=%d evidence=%d want 1/1", artifacts, evidence)
	}

	rec := f.record(t, second.ID)
	if rec.Caption != "take 2" || rec.ContentHash != artifactContentHash(frameTwo) {
		t.Fatalf("record did not follow the new content: %+v", rec)
	}
	// Superseded bytes have no durable record.
	if _, ok := readArtifactBlob(f.blobDir(t), artifactContentHash(frameOne), int64(len(frameOne))); ok {
		t.Fatal("superseded blob survived a replacement")
	}
}

func TestDurableStoreNaturalKeyReplacesAcrossRestart(t *testing.T) {
	f := newDurableFixture(t, []string{"root-1"})
	slot := "live-tool-recording/root-1/root-1/page-1"
	first, err := f.store.Put(t.Context(), "root-1", Entry{
		Meta:        api.VisualArtifact{Mime: "video/mp4", Source: api.VisualArtifactSourceCapture, PageID: "page-1"},
		OperationID: uuid.NewString(),
		NaturalKey:  slot,
		Bytes:       []byte("recording-one"),
	})
	testutil.FailErr(t, "first recording", err)

	// A fresh store over the same database is a restarted sidecar.
	restarted := NewDurableStore(DurableConfig{
		ArtifactsDir: f.store.artifactsDir,
		Lookup:       func(context.Context, string) (string, error) { return f.project, nil },
		Records:      NewRecords(f.sqlDB, eventoutbox.New(f.sqlDB, nil), testProjection()),
	})
	second, err := restarted.Put(t.Context(), "root-1", Entry{
		Meta:        api.VisualArtifact{Mime: "video/mp4", Source: api.VisualArtifactSourceCapture, PageID: "page-1"},
		OperationID: uuid.NewString(),
		NaturalKey:  slot,
		Bytes:       []byte("recording-two"),
	})
	testutil.FailErr(t, "second recording", err)
	if first.ID != second.ID {
		t.Fatalf("slot %s multiplied after restart: %s vs %s", slot, first.ID, second.ID)
	}
	res := restarted.Resolve(t.Context(), "root-1", second.ID)
	if !res.IsPresent() || string(res.Bytes()) != "recording-two" {
		t.Fatalf("re-recorded slot = %q present=%v", res.Bytes(), res.IsPresent())
	}
}

func TestDurableStorePutStampsActiveWorkflowRun(t *testing.T) {
	f := newDurableFixture(t, []string{"root-1"})
	const runID = "run-42"
	insertWorkflowRun(t, f.sqlDB, runID, "root-1", f.project)
	f.store.activeRun = func(context.Context, string) (string, error) { return runID, nil }
	wire, err := f.store.Put(t.Context(), "root-1", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture},
		Bytes: onePixelPNG(t),
	})
	testutil.FailErr(t, "Put failed", err)
	if got := f.record(t, wire.ID).WorkflowRunID; got != runID {
		t.Fatalf("workflow_run_id = %q want %q", got, runID)
	}
	var legID string
	testutil.FailErr(t, "read leg_id", f.sqlDB.QueryRowContext(t.Context(),
		`SELECT COALESCE(leg_id, '') FROM evidence_index WHERE source_ref = ?`, wire.ID).Scan(&legID))
	if legID != runID {
		t.Fatalf("evidence_index.leg_id = %q want %q", legID, runID)
	}
	items, err := f.store.ListTree(t.Context(), "root-1")
	testutil.FailErr(t, "ListTree failed", err)
	if len(items) != 1 || items[0].WorkflowRunID != runID {
		t.Fatalf("tree list = %+v want run-scoped rows", items)
	}
}

func TestDurableStoreIdenticalBytesOneBlobTwoRecords(t *testing.T) {
	f := newDurableFixture(t, []string{"root-1"})
	a, err := f.store.Put(t.Context(), "root-1", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender, Caption: "a"},
		Bytes: onePixelPNG(t),
	})
	testutil.FailErr(t, "Put a failed", err)
	b, err := f.store.Put(t.Context(), "root-1", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender, Caption: "b"},
		Bytes: onePixelPNG(t),
	})
	testutil.FailErr(t, "Put b failed", err)
	if a.ID == b.ID {
		t.Fatal("want distinct ids")
	}
	ra, rb := f.record(t, a.ID), f.record(t, b.ID)
	if ra.ContentHash != rb.ContentHash {
		t.Fatalf("hashes differ: %q vs %q", ra.ContentHash, rb.ContentHash)
	}
	entries, err := os.ReadDir(f.blobDir(t))
	testutil.FailErr(t, "read artifact blobs", err)
	if len(entries) != 1 || entries[0].Name() != ra.ContentHash {
		t.Fatalf("artifact blobs = %v want one shared hash", entries)
	}
}

func TestDurableStoreResolveFallsBackAfterHotDrop(t *testing.T) {
	f := newDurableFixture(t, []string{"root-1"})
	chunk := append(onePixelPNG(t), make([]byte, 512*1024)...)
	var firstID string
	for i := 0; i < 260; i++ {
		chunk[len(chunk)-1] = byte(i)
		wire, err := f.store.Put(t.Context(), "root-1", Entry{
			Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender},
			Bytes: chunk,
		})
		testutil.FailErr(t, "Put failed", err)
		if i == 0 {
			firstID = wire.ID
		}
	}
	res := f.store.Resolve(t.Context(), "root-1", firstID)
	if !res.IsPresent() || len(res.Bytes()) != len(chunk) {
		t.Fatalf("overlay fallback: present=%v bytes=%d", res.IsPresent(), len(res.Bytes()))
	}
}

func TestDurableStoreRestartSurvival(t *testing.T) {
	f := newDurableFixture(t, []string{"root-1"})
	wire, err := f.store.Put(t.Context(), "root-1", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture, EvidenceHandle: "page#1"},
		Bytes: onePixelPNG(t),
	})
	testutil.FailErr(t, "Put failed", err)
	fresh := f.restart()
	res := fresh.Resolve(t.Context(), "root-1", wire.ID)
	if !res.IsPresent() || len(res.Bytes()) == 0 || res.Meta().EvidenceHandle != "page#1" {
		t.Fatalf("restart resolve = %+v", res)
	}
}

func insertWorkflowRun(t *testing.T, sqlDB db.Handle, runID, sessionID, projectID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := sqlDB.ExecContext(t.Context(), `
		INSERT INTO workflow_runs (id, session_id, project_id, workflow_id, workflow_version, status, current_phase, created_at, updated_at)
		VALUES (?, ?, ?, 'wf', '1', 'running', '', ?, ?)
	`, runID, sessionID, projectID, now, now)
	testutil.FailErr(t, "insert workflow run", err)
}
