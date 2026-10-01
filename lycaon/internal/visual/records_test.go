package visual

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func recordFor(f durableFixture, id string, body []byte) ArtifactRecord {
	return ArtifactRecord{
		ID:            id,
		ProjectID:     f.project,
		RootSessionID: "root-1",
		SessionID:     "root-1",
		ContentHash:   artifactContentHash(body),
		ByteSize:      int64(len(body)),
		Mime:          "image/png",
		Source:        string(api.VisualArtifactSourceRender),
	}
}

func countRows(t *testing.T, sqlDB db.Handle, query string, args ...any) int {
	t.Helper()
	var n int
	testutil.FailErr(t, "count rows", sqlDB.QueryRowContext(t.Context(), query, args...).Scan(&n))
	return n
}

func TestRecordsCommitRollsBackWhenProjectionFails(t *testing.T) {
	f := newDurableFixture(t, []string{"root-1"})
	boom := errors.New("projection write failed")
	records := NewRecords(f.sqlDB, eventoutbox.New(f.sqlDB, nil), ArtifactProjection{
		Write: func(context.Context, *sql.Tx, string, ArtifactRecord) error { return boom },
	})

	err := records.Commit(t.Context(), recordFor(f, "art-projection", []byte("bytes")))
	if !errors.Is(err, boom) {
		t.Fatalf("Commit error = %v want the projection failure", err)
	}
	if n := countRows(t, f.sqlDB, `SELECT COUNT(*) FROM artifacts WHERE id = ?`, "art-projection"); n != 0 {
		t.Fatalf("artifacts rows = %d want 0 after a failed projection", n)
	}
	if n := countRows(t, f.sqlDB, `SELECT COUNT(*) FROM event_outbox`); n != 0 {
		t.Fatalf("event_outbox rows = %d want 0 after a failed projection", n)
	}
}

func TestRecordsCommitRollsBackWhenOutboxFails(t *testing.T) {
	f := newDurableFixture(t, []string{"root-1"})
	_, err := f.sqlDB.ExecContext(t.Context(), `DROP TABLE event_outbox`)
	testutil.FailErr(t, "drop event_outbox", err)
	records := NewRecords(f.sqlDB, eventoutbox.New(f.sqlDB, nil), testProjection())

	if err := records.Commit(t.Context(), recordFor(f, "art-outbox", []byte("bytes"))); err == nil {
		t.Fatal("Commit succeeded with no event plane to announce it")
	}
	if n := countRows(t, f.sqlDB, `SELECT COUNT(*) FROM artifacts WHERE id = ?`, "art-outbox"); n != 0 {
		t.Fatalf("artifacts rows = %d want 0 after a failed enqueue", n)
	}
	if n := countRows(t, f.sqlDB, `SELECT COUNT(*) FROM evidence_index WHERE source_ref = ?`, "art-outbox"); n != 0 {
		t.Fatalf("evidence_index rows = %d want 0 after a failed enqueue", n)
	}
}

func TestRecordsRefuseToWriteWithNoOutbox(t *testing.T) {
	f := newDurableFixture(t, []string{"root-1"})
	records := NewRecords(f.sqlDB, nil, testProjection())
	if err := records.Commit(t.Context(), recordFor(f, "art-unwired", []byte("bytes"))); !errors.Is(err, eventoutbox.ErrNotWired) {
		t.Fatalf("Commit error = %v want ErrNotWired", err)
	}
	if _, _, err := records.SoftDelete(t.Context(), f.project, "art-unwired", "human_delete"); !errors.Is(err, eventoutbox.ErrNotWired) {
		t.Fatalf("SoftDelete error = %v want ErrNotWired", err)
	}
}

func TestRecordsCommitRefusesToReviveATombstone(t *testing.T) {
	f := newDurableFixture(t, []string{"root-1"})
	records := NewRecords(f.sqlDB, eventoutbox.New(f.sqlDB, nil), testProjection())
	rec := recordFor(f, "art-tombstoned", []byte("bytes"))
	testutil.FailErr(t, "first commit", records.Commit(t.Context(), rec))
	_, _, err := records.SoftDelete(t.Context(), f.project, rec.ID, "human_delete")
	testutil.FailErr(t, "soft delete", err)

	if err := records.Commit(t.Context(), rec); !errors.Is(err, ErrArtifactDeleted) {
		t.Fatalf("re-commit error = %v want ErrArtifactDeleted", err)
	}
	stored, found, err := records.GetInProject(t.Context(), f.project, rec.ID)
	testutil.FailErr(t, "reload record", err)
	if !found || !stored.Deleted() {
		t.Fatalf("record after re-commit: found=%v deleted=%v want a surviving tombstone", found, stored.Deleted())
	}
}

func TestRecordsClaimedIDSkipsTombstones(t *testing.T) {
	f := newDurableFixture(t, []string{"root-1"})
	records := NewRecords(f.sqlDB, eventoutbox.New(f.sqlDB, nil), testProjection())
	rec := recordFor(f, "art-claimed", []byte("bytes"))
	rec.OperationID, rec.NaturalKey = "op-1", "slot-1"
	testutil.FailErr(t, "commit", records.Commit(t.Context(), rec))

	claimed, err := records.ClaimedID(t.Context(), f.project, "op-1", "slot-1")
	testutil.FailErr(t, "ClaimedID before delete", err)
	if claimed != rec.ID {
		t.Fatalf("ClaimedID = %q want %q", claimed, rec.ID)
	}
	_, _, err = records.SoftDelete(t.Context(), f.project, rec.ID, "human_delete")
	testutil.FailErr(t, "soft delete", err)

	claimed, err = records.ClaimedID(t.Context(), f.project, "op-1", "slot-1")
	testutil.FailErr(t, "ClaimedID after delete", err)
	if claimed != "" {
		t.Fatalf("ClaimedID = %q want empty so the write mints a new id", claimed)
	}
	// A successor can claim both keys.
	next := recordFor(f, "art-successor", []byte("more bytes"))
	next.OperationID, next.NaturalKey = "op-1", "slot-1"
	testutil.FailErr(t, "successor commit", records.Commit(t.Context(), next))
}

func TestRecordsFindByEvidenceHandleReturnsNewestLive(t *testing.T) {
	f := newDurableFixture(t, []string{"root-1"})
	records := NewRecords(f.sqlDB, eventoutbox.New(f.sqlDB, nil), testProjection())
	older := recordFor(f, "art-z-older", []byte("older"))
	older.EvidenceHandle, older.CreatedAt = "capture#1", time.Now().UTC().Format(time.RFC3339Nano)
	testutil.FailErr(t, "commit older", records.Commit(t.Context(), older))
	newer := recordFor(f, "art-a-newer", []byte("newer"))
	newer.EvidenceHandle, newer.CreatedAt = "capture#1", older.CreatedAt
	testutil.FailErr(t, "commit newer", records.Commit(t.Context(), newer))

	got, found, err := records.FindByEvidenceHandle(t.Context(), "root-1", "capture#1")
	testutil.FailErr(t, "FindByEvidenceHandle", err)
	if !found || got.ID != newer.ID {
		t.Fatalf("handle resolved to %q found=%v want %q", got.ID, found, newer.ID)
	}
	_, _, err = records.SoftDelete(t.Context(), f.project, newer.ID, "human_delete")
	testutil.FailErr(t, "soft delete newest", err)
	got, found, err = records.FindByEvidenceHandle(t.Context(), "root-1", "capture#1")
	testutil.FailErr(t, "FindByEvidenceHandle after delete", err)
	if !found || got.ID != older.ID {
		t.Fatalf("handle after delete resolved to %q found=%v want %q", got.ID, found, older.ID)
	}
}
