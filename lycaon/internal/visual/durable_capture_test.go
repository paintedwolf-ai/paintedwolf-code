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
	"github.com/lycaon/lycaon/internal/bloblifecycle"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestArtifactDeletionRetainsCapturedBodyUntilArchiveCompletes(t *testing.T) {
	f := newDurableFixture(t, []string{"session"})
	artifact, err := f.store.Put(t.Context(), "session", Entry{
		Meta: api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender}, Bytes: onePixelPNG(t),
	})
	testutil.FailErr(t, "publish captured artifact", err)
	record := f.record(t, artifact.ID)
	release := bloblifecycle.AcquirePublication(f.dataDir)
	_, found, err := f.store.Delete(t.Context(), f.project, artifact.ID, "human_delete")
	if err != nil || !found {
		release()
		t.Fatalf("delete captured artifact: found=%v err=%v", found, err)
	}
	retained := f.blobExists(t, record)
	release()
	if !retained {
		t.Fatal("artifact bytes disappeared during archive capture")
	}
	testutil.FailErr(t, "reconcile after archive", f.store.reconcileProjectStorage(t.Context(), f.project, f.blobDir(t)))
	if f.blobExists(t, record) {
		t.Fatal("unreferenced artifact bytes survived completed archive capture")
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
