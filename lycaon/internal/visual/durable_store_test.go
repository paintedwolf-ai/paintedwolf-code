package visual

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

func TestDurableStoreFirstAccessPrunesOrphanBlobs(t *testing.T) {
	f := newDurableFixture(t, []string{"root-1"})
	wire, err := f.store.Put(t.Context(), "root-1", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender},
		Bytes: onePixelPNG(t),
	})
	testutil.FailErr(t, "Put failed", err)
	orphan := []byte("orphaned artifact")
	orphanHash := artifactContentHash(orphan)
	_, _, err = putArtifactBlob(f.blobDir(t), orphanHash, orphan)
	testutil.FailErr(t, "write orphan blob", err)

	fresh := f.restart()
	if res := fresh.Resolve(t.Context(), "root-1", wire.ID); !res.IsPresent() {
		t.Fatalf("live artifact resolved as %q", res.Reason())
	}
	if _, ok := readArtifactBlob(f.blobDir(t), orphanHash, int64(len(orphan))); ok {
		t.Fatal("orphan blob survived reconciliation")
	}
}

func TestArtifactBlobPruningIsBounded(t *testing.T) {
	dir := t.TempDir()
	for i := range artifactPruneBatchSize + 1 {
		path := filepath.Join(dir, fmt.Sprintf("%064x", i+1))
		testutil.FailErr(t, "write orphan blob", os.WriteFile(path, []byte("orphan"), 0o600))
	}
	state := &artifactPruneState{}
	checked := 0
	isOrphan := func(string) (bool, error) {
		checked++
		return true, nil
	}
	done, err := pruneArtifactBlobBatch(dir, state, isOrphan)
	testutil.FailErr(t, "prune first batch", err)
	if done || checked != artifactPruneBatchSize {
		t.Fatalf("first batch: done=%v checked=%d", done, checked)
	}
	done, err = pruneArtifactBlobBatch(dir, state, isOrphan)
	testutil.FailErr(t, "prune second batch", err)
	if !done || checked != artifactPruneBatchSize+1 {
		t.Fatalf("second batch: done=%v checked=%d", done, checked)
	}
}

func TestDurableStoreListProjectAndTree(t *testing.T) {
	f := newDurableFixture(t, []string{"tree-a", "tree-b"})
	_, err := f.store.Put(t.Context(), "tree-a", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender, Caption: "a"},
		Bytes: onePixelPNG(t),
	})
	testutil.FailErr(t, "Put tree-a failed", err)
	_, err = f.store.Put(t.Context(), "tree-b", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture, Caption: "b", EvidenceHandle: "page#2"},
		Bytes: append(onePixelPNG(t), 0),
	})
	testutil.FailErr(t, "Put tree-b failed", err)

	page, err := f.store.ListProject(t.Context(), f.project, ArtifactPageQuery{})
	testutil.FailErr(t, "ListProject failed", err)
	all := page.Items
	if len(all) != 2 {
		t.Fatalf("ListProject = %d want 2", len(all))
	}
	for _, art := range all {
		if art.SessionID == "" || art.CreatedAt.IsZero() {
			t.Fatalf("list item missing session/created: %+v", art)
		}
	}
	tree, err := f.store.ListTree(t.Context(), "tree-a")
	testutil.FailErr(t, "ListTree failed", err)
	if len(tree) != 1 || tree[0].Caption != "a" || tree[0].SessionID != "tree-a" {
		t.Fatalf("ListTree = %+v", tree)
	}
}

func TestDurableStoreListsDraftArtifactsWithoutProject(t *testing.T) {
	store := NewDurableStore(DurableConfig{
		Lookup: func(context.Context, string) (string, error) { return "", nil },
	})
	wire, err := store.Put(t.Context(), "draft-root", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture, Caption: "draft"},
		Bytes: onePixelPNG(t),
	})
	testutil.FailErr(t, "put draft artifact", err)
	items, err := store.ListTree(t.Context(), "draft-root")
	testutil.FailErr(t, "list draft artifacts", err)
	if len(items) != 1 || items[0].ID != wire.ID || items[0].Caption != "draft" {
		t.Fatalf("draft artifacts = %+v", items)
	}
	if res := store.Resolve(t.Context(), "draft-root", wire.ID); !res.IsPresent() {
		t.Fatalf("draft artifact resolution = %+v", res)
	}
}

func TestDurableStoreDoesNotHideProjectLookupFailure(t *testing.T) {
	lookupErr := errors.New("project lookup failed")
	store := NewDurableStore(DurableConfig{
		Lookup: func(context.Context, string) (string, error) { return "", lookupErr },
	})
	if _, err := store.Put(t.Context(), "root", Entry{
		Meta: api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture}, Bytes: onePixelPNG(t),
	}); !errors.Is(err, lookupErr) {
		t.Fatalf("put error = %v", err)
	}
	if _, err := store.ListTree(t.Context(), "root"); !errors.Is(err, lookupErr) {
		t.Fatalf("list error = %v", err)
	}
	if res := store.Resolve(t.Context(), "root", "artifact"); res.Reason() != AbsenceUnavailable {
		t.Fatalf("resolve reason = %q", res.Reason())
	}
}

func TestDurableStoreListProjectUsesStableSeekPages(t *testing.T) {
	f := newDurableFixture(t, []string{"tree-a"})
	for _, caption := range []string{"a", "b", "c"} {
		_, err := f.store.Put(t.Context(), "tree-a", Entry{
			Meta: api.VisualArtifact{
				Mime: "image/png", Source: api.VisualArtifactSourceRender, Caption: caption,
			},
			Bytes: append(onePixelPNG(t), caption...),
		})
		testutil.FailErr(t, "Put "+caption, err)
	}
	first, err := f.store.ListProject(t.Context(), f.project, ArtifactPageQuery{Limit: 2})
	testutil.FailErr(t, "list first page", err)
	if len(first.Items) != 2 || first.NextCreatedAt == "" || first.NextID == "" {
		t.Fatalf("first page = %+v want two items and a cursor", first)
	}
	second, err := f.store.ListProject(t.Context(), f.project, ArtifactPageQuery{
		AfterCreatedAt: first.NextCreatedAt, AfterID: first.NextID, Limit: 2,
	})
	testutil.FailErr(t, "list second page", err)
	if len(second.Items) != 1 || second.NextCreatedAt != "" || second.NextID != "" {
		t.Fatalf("second page = %+v want one terminal item", second)
	}
	seen := map[string]struct{}{}
	for _, item := range append(first.Items, second.Items...) {
		if _, duplicate := seen[item.ID]; duplicate {
			t.Fatalf("artifact %s appeared in both pages", item.ID)
		}
		seen[item.ID] = struct{}{}
	}
	if len(seen) != 3 {
		t.Fatalf("paged artifacts = %d want 3", len(seen))
	}
}

func TestDurableStoreKeepsEveryRecordedArtifactBlob(t *testing.T) {
	f := newDurableFixture(t, []string{"root-1"})
	cited, err := f.store.Put(t.Context(), "root-1", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture, EvidenceHandle: "page#9"},
		Bytes: append(onePixelPNG(t), make([]byte, 800)...),
	})
	testutil.FailErr(t, "cited Put failed", err)
	claimed, err := f.store.Put(t.Context(), "root-1", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender},
		Bytes: append(onePixelPNG(t), append(make([]byte, 799), 1)...),
	})
	testutil.FailErr(t, "claimed Put failed", err)
	// The project cover is this artifact's only reference.
	testutil.FailErr(t, "cover claim failed", WriteRefsTx(t.Context(), f.queries, []RefWrite{{
		Kind: api.ArtifactReferenceKindProjectCover, ArtifactID: claimed.ID, ProjectID: f.project,
	}}))

	for i := 0; i < 8; i++ {
		body := append(onePixelPNG(t), make([]byte, 800)...)
		body[len(body)-1] = byte(i + 2)
		_, err := f.store.Put(t.Context(), "root-1", Entry{
			Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender},
			Bytes: body,
		})
		testutil.FailErr(t, "filler Put failed", err)
	}

	if !f.blobExists(t, f.record(t, cited.ID)) {
		t.Fatal("cited (evidence_handle) blob was pruned")
	}
	if !f.blobExists(t, f.record(t, claimed.ID)) {
		t.Fatal("claimed (project_cover reference) blob was pruned")
	}
	entries, _ := os.ReadDir(f.blobDir(t))
	blobs := 0
	for _, e := range entries {
		if !e.IsDir() {
			blobs++
		}
	}
	if blobs != 10 {
		t.Fatalf("recorded artifact blob count=%d want 10", blobs)
	}
}

func TestDurableStoreDeleteTombstonesAndReportsReferences(t *testing.T) {
	f := newDurableFixture(t, []string{"root-1"})
	wire, err := f.store.Put(t.Context(), "root-1", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender, Caption: "doomed"},
		Bytes: onePixelPNG(t),
	})
	testutil.FailErr(t, "Put failed", err)
	testutil.FailErr(t, "cover claim failed", WriteRefsTx(t.Context(), f.queries, []RefWrite{{
		Kind: api.ArtifactReferenceKindProjectCover, ArtifactID: wire.ID, ProjectID: f.project,
	}}))

	result, found, err := f.store.Delete(t.Context(), f.project, wire.ID, "human_delete")
	testutil.FailErr(t, "Delete failed", err)
	if !found {
		t.Fatal("delete reported no such artifact")
	}
	if len(result.References) != 1 || result.References[0].Kind != api.ArtifactReferenceKindProjectCover {
		t.Fatalf("references = %+v want the cover claim", result.References)
	}
	if result.DeletedAt.IsZero() {
		t.Fatal("deleted_at not stamped")
	}

	// Tombstoned identities remain unavailable.
	res := f.store.Resolve(t.Context(), "root-1", wire.ID)
	if res.IsPresent() || res.Reason() != AbsenceDeleted {
		t.Fatalf("resolve after delete = present:%v reason:%q want deleted", res.IsPresent(), res.Reason())
	}
	if f.blobExists(t, f.record(t, wire.ID)) {
		t.Fatal("deleted artifact bytes survived")
	}
	page, err := f.store.ListProject(t.Context(), f.project, ArtifactPageQuery{})
	testutil.FailErr(t, "ListProject failed", err)
	items := page.Items
	if len(items) != 0 {
		t.Fatalf("deleted artifact still listed: %+v", items)
	}
}

func TestDurableStoreDiscardRemovesUnadmittedArtifact(t *testing.T) {
	f := newDurableFixture(t, []string{"root-1"})
	wire, err := f.store.Put(t.Context(), "root-1", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceUser},
		Bytes: onePixelPNG(t),
	})
	testutil.FailErr(t, "Put failed", err)
	hash := f.record(t, wire.ID).ContentHash

	testutil.FailErr(t, "Discard failed", f.store.Discard(t.Context(), f.project, wire.ID))
	if _, found, getErr := f.store.records.GetInProject(t.Context(), f.project, wire.ID); getErr != nil || found {
		t.Fatalf("record after discard: found=%v err=%v", found, getErr)
	}
	var projected int
	testutil.FailErr(t, "count evidence rows", f.sqlDB.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM evidence_index WHERE source_ref = ?`, wire.ID).Scan(&projected))
	if projected != 0 {
		t.Fatalf("evidence_index rows = %d want 0", projected)
	}
	if _, ok := readArtifactBlob(f.blobDir(t), hash, int64(len(onePixelPNG(t)))); ok {
		t.Fatal("discarded artifact bytes survived")
	}
	if res := f.store.Resolve(t.Context(), "root-1", wire.ID); res.IsPresent() || res.Reason() != AbsenceUnknown {
		t.Fatalf("resolve after discard = present:%v reason:%q", res.IsPresent(), res.Reason())
	}
}

func TestDurableStoreReRecordAfterDeleteMintsANewID(t *testing.T) {
	f := newDurableFixture(t, []string{"root-1"})
	const slot = "live-tool-recording/root-1/root-1/page-1"
	operation := uuid.NewString()
	entry := func() Entry {
		return Entry{
			Meta:        api.VisualArtifact{Mime: "video/mp4", Source: api.VisualArtifactSourceCapture, PageID: "page-1"},
			OperationID: operation,
			NaturalKey:  slot,
			Bytes:       []byte("recording-one"),
		}
	}
	first, err := f.store.Put(t.Context(), "root-1", entry())
	testutil.FailErr(t, "first recording", err)
	_, found, err := f.store.Delete(t.Context(), f.project, first.ID, "human_delete")
	testutil.FailErr(t, "delete recording", err)
	if !found {
		t.Fatal("delete reported no such artifact")
	}

	// Tombstones release both identity keys.
	again := entry()
	again.Bytes = []byte("recording-two")
	second, err := f.store.Put(t.Context(), "root-1", again)
	testutil.FailErr(t, "re-recording after delete", err)
	if second.ID == first.ID {
		t.Fatal("re-recording revived the artifact a human deleted")
	}
	if res := f.store.Resolve(t.Context(), "root-1", first.ID); res.IsPresent() || res.Reason() != AbsenceDeleted {
		t.Fatalf("deleted id = present:%v reason:%q want deleted", res.IsPresent(), res.Reason())
	}
	if res := f.store.Resolve(t.Context(), "root-1", second.ID); !res.IsPresent() || string(res.Bytes()) != "recording-two" {
		t.Fatalf("new recording = present:%v bytes:%q", res.IsPresent(), res.Bytes())
	}
	page, err := f.store.ListProject(t.Context(), f.project, ArtifactPageQuery{})
	testutil.FailErr(t, "ListProject failed", err)
	items := page.Items
	if len(items) != 1 || items[0].ID != second.ID {
		t.Fatalf("project list = %+v want only the new recording", items)
	}
}

func TestDurableStorePutOnDeletedIDMintsANewOne(t *testing.T) {
	f := newDurableFixture(t, []string{"root-1"})
	first, err := f.store.Put(t.Context(), "root-1", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender},
		Bytes: onePixelPNG(t),
	})
	testutil.FailErr(t, "Put failed", err)
	_, _, err = f.store.Delete(t.Context(), f.project, first.ID, "human_delete")
	testutil.FailErr(t, "Delete failed", err)

	second, err := f.store.Put(t.Context(), "root-1", Entry{
		Meta:  api.VisualArtifact{ID: first.ID, Mime: "image/png", Source: api.VisualArtifactSourceRender},
		Bytes: append(onePixelPNG(t), 7),
	})
	testutil.FailErr(t, "Put on a deleted id", err)
	if second.ID == first.ID {
		t.Fatal("a write reusing a deleted id revived it")
	}
	if rec := f.record(t, first.ID); !rec.Deleted() {
		t.Fatal("tombstone was cleared")
	}
}

func TestDurableStorePutLeavesNoHotBytesWhenTheCommitFails(t *testing.T) {
	f := newDurableFixture(t, []string{"root-1"})
	hot := NewMemoryStore()
	store := NewDurableStore(DurableConfig{
		ArtifactsDir: f.store.artifactsDir,
		Lookup:       func(context.Context, string) (string, error) { return f.project, nil },
		Hot:          hot,
		Records: NewRecords(f.sqlDB, eventoutbox.New(f.sqlDB, nil), ArtifactProjection{
			Write: func(context.Context, *sql.Tx, string, ArtifactRecord) error {
				return errors.New("projection write failed")
			},
		}),
	})
	const id = "art-uncommitted"
	if _, err := store.Put(t.Context(), "root-1", Entry{
		Meta:  api.VisualArtifact{ID: id, Mime: "image/png", Source: api.VisualArtifactSourceRender},
		Bytes: onePixelPNG(t),
	}); err == nil {
		t.Fatal("Put succeeded with a failing projection")
	}
	if res := hot.Resolve(t.Context(), "root-1", id); res.IsPresent() {
		t.Fatal("hot cache answers for an artifact that never committed")
	}
	if n := countRows(t, f.sqlDB, `SELECT COUNT(*) FROM artifacts WHERE id = ?`, id); n != 0 {
		t.Fatalf("artifacts rows = %d want 0", n)
	}
}

func TestDurableStoreFindByEvidenceHandleAcrossRestart(t *testing.T) {
	f := newDurableFixture(t, []string{"root-1"})
	wire, err := f.store.Put(t.Context(), "root-1", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture, EvidenceHandle: "capture#7"},
		Bytes: onePixelPNG(t),
	})
	testutil.FailErr(t, "Put failed", err)
	fresh := NewDurableStore(DurableConfig{
		ArtifactsDir: f.store.artifactsDir,
		Lookup:       func(context.Context, string) (string, error) { return f.project, nil },
		Records:      NewRecords(f.sqlDB, eventoutbox.New(f.sqlDB, nil), testProjection()),
	})
	id, res := ResolveRef(t.Context(), fresh, "root-1", "capture#7")
	if id != wire.ID || !res.IsPresent() {
		t.Fatalf("handle after restart: id=%q present=%v want %q", id, res.IsPresent(), wire.ID)
	}
}

func TestDurableStoreListCarriesReferenceCountsAndOrigins(t *testing.T) {
	f := newDurableFixture(t, []string{"root-1"})
	wire, err := f.store.Put(t.Context(), "root-1", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender},
		Bytes: onePixelPNG(t),
	})
	testutil.FailErr(t, "Put failed", err)
	insertMessageRow(t, f.sqlDB, "msg-1", "root-1")
	testutil.FailErr(t, "message claim failed", WriteRefsTx(t.Context(), f.queries, []RefWrite{{
		Kind: api.ArtifactReferenceKindMessagePresent, ArtifactID: wire.ID, ProjectID: f.project,
		MessageID: "msg-1", SessionID: "root-1",
	}}))

	page, err := f.store.ListProject(t.Context(), f.project, ArtifactPageQuery{})
	testutil.FailErr(t, "ListProject failed", err)
	items := page.Items
	if len(items) != 1 {
		t.Fatalf("items = %d want 1", len(items))
	}
	if items[0].OriginMessageID != "msg-1" {
		t.Fatalf("origin_message_id = %q want msg-1", items[0].OriginMessageID)
	}
	if len(items[0].References) != 1 || items[0].References[0].Count != 1 ||
		items[0].References[0].Kind != api.ArtifactReferenceKindMessagePresent {
		t.Fatalf("references = %+v want one message_present claim", items[0].References)
	}
}

// Tool-call references provide transcript coordinates for tool results.
func TestDurableStoreListRecoversToolCallFromClaimingRef(t *testing.T) {
	f := newDurableFixture(t, []string{"root-1"})
	wire, err := f.store.Put(t.Context(), "root-1", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture},
		Bytes: onePixelPNG(t),
	})
	testutil.FailErr(t, "Put failed", err)
	if wire.ToolCallID != "" {
		t.Fatalf("fixture precondition: stored meta tool_call_id = %q want empty", wire.ToolCallID)
	}
	insertMessageRow(t, f.sqlDB, "msg-tool-result", "root-1")
	testutil.FailErr(t, "tool claim failed", WriteRefsTx(t.Context(), f.queries, []RefWrite{{
		Kind: api.ArtifactReferenceKindToolResult, ArtifactID: wire.ID, ProjectID: f.project,
		MessageID: "msg-tool-result", SessionID: "root-1", ToolCallID: "call-9",
	}}))

	page, err := f.store.ListProject(t.Context(), f.project, ArtifactPageQuery{})
	testutil.FailErr(t, "ListProject failed", err)
	items := page.Items
	if len(items) != 1 {
		t.Fatalf("items = %d want 1", len(items))
	}
	if items[0].ToolCallID != "call-9" {
		t.Fatalf("tool_call_id = %q want call-9", items[0].ToolCallID)
	}
	if items[0].OriginMessageID != "msg-tool-result" {
		t.Fatalf("origin_message_id = %q want msg-tool-result", items[0].OriginMessageID)
	}
}

func TestDurableStorePutRejectsForeignTreeIdentity(t *testing.T) {
	f := newDurableFixture(t, []string{"root-a", "root-b"})
	wire, err := f.store.Put(t.Context(), "root-a", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender},
		Bytes: onePixelPNG(t),
	})
	testutil.FailErr(t, "Put failed", err)
	if _, err := f.store.Put(t.Context(), "root-b", Entry{
		Meta:  api.VisualArtifact{ID: wire.ID, Mime: "image/png", Source: api.VisualArtifactSourceRender},
		Bytes: append(onePixelPNG(t), "someone else's bytes"...),
	}); err == nil {
		t.Fatal("a foreign tree overwrote an artifact by reusing its id")
	}
}

func TestDurableStoreStorageUsageReportsProjectBytes(t *testing.T) {
	f := newDurableFixture(t, []string{"root-1"})
	raw := append(onePixelPNG(t), make([]byte, 1024-len(onePixelPNG(t)))...)
	_, err := f.store.Put(t.Context(), "root-1", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender},
		Bytes: raw,
	})
	testutil.FailErr(t, "Put failed", err)
	_, err = f.store.Put(t.Context(), "root-1", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender},
		Bytes: raw,
	})
	testutil.FailErr(t, "Put duplicate content failed", err)
	lane, err := f.store.StorageUsage(t.Context(), f.project)
	testutil.FailErr(t, "StorageUsage failed", err)
	info, err := os.Stat(filepath.Join(f.blobDir(t), artifactContentHash(raw)))
	testutil.FailErr(t, "stat encoded artifact", err)
	if lane.UsedBytes != info.Size() || lane.UsedBytes >= int64(len(raw)) {
		t.Fatalf("lane = %+v want %d compressed deduplicated bytes", lane, info.Size())
	}
}

func insertMessageRow(t *testing.T, sqlDB db.Handle, messageID, sessionID string) {
	t.Helper()
	testdbseed.InsertSessionEntry(t, sqlDB, "entry-"+messageID, sessionID, "model_output", messageID, 1)
	_, err := sqlDB.ExecContext(t.Context(), `
		INSERT INTO messages (id, entry_id, session_id, role, content, origin, authority, trust_tier, ts)
		VALUES (?, ?, ?, 'assistant', '', 'model', 'none', 'trusted', ?)
	`, messageID, "entry-"+messageID, sessionID, time.Now().UTC().Format(time.RFC3339))
	testutil.FailErr(t, "insert message row", err)
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
