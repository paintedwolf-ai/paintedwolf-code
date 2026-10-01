package editordoc

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSaveReservationSurvivesEvictionAndTransfersToJournal(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "base"})
	d := f.open(t, "a.txt")
	operation := uuid.NewString()
	pin, err := f.service.PinSave(t.Context(), f.project.ID, d.ID, "window", operation)
	testutil.FailErr(t, "reserve original draft", err)
	changed, err := f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision}, Content: "later typing", EOL: "lf"})
	testutil.FailErr(t, "type after pin", err)
	replay, err := f.service.PinSave(t.Context(), f.project.ID, d.ID, "window", operation)
	testutil.FailErr(t, "retry pin after typing", err)
	if replay.Draft != pin.Draft || replay.Revision != pin.Revision {
		t.Fatal("pin retry followed later typing")
	}
	_, err = f.service.PinSave(t.Context(), f.project.ID, d.ID, "another-window", operation)
	if !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("foreign pin = %v", err)
	}
	head, err := f.store.replicaHead(t.Context(), d.ID)
	testutil.FailErr(t, "read replica head", err)
	for i := int64(0); i < 300; i++ {
		snapshot := *changed
		snapshot.Revision += i
		testutil.FailErr(t, "churn read cache", f.store.pinSnapshot(t.Context(), &snapshot, head, head.Checkpoint, nil))
	}
	var count int
	testutil.FailErr(t, "count bounded snapshots", f.store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM editor_document_snapshots`).Scan(&count))
	if count > 257 {
		t.Fatalf("retained %d snapshots, want at most 256 reads plus reservation", count)
	}
	saved, err := f.service.Save(t.Context(), f.project, d.ID, "window", operation, "", 0, pin.Revision)
	testutil.FailErr(t, "save reserved snapshot", err)
	if f.disk(t, "a.txt") != "base" || saved.Draft != "later typing" || !saved.Dirty {
		t.Fatalf("save lost exact intent: %+v", saved)
	}
	testutil.FailErr(t, "count transferred pins", f.store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM editor_save_pins`).Scan(&count))
	if count != 0 {
		t.Fatal("completed save retained a reservation")
	}
	testutil.FailErr(t, "evict every read snapshot", f.store.Tx(t.Context(), func(tx *sql.Tx) error {
		_, e := tx.ExecContext(t.Context(), `DELETE FROM editor_document_snapshots`)
		return e
	}))
	_, err = f.service.Save(t.Context(), f.project, d.ID, "window", operation, "", 0, pin.Revision)
	testutil.FailErr(t, "replay from durable journal without snapshot", err)
}

func TestSnapshotPayloadCompressesAndPreservesMetadata(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": strings.Repeat("func example() { return value }\n", 16384)})
	d := f.open(t, "a.txt")
	pinned, err := f.service.Pin(t.Context(), f.project.ID, d.ID, d.Revision)
	testutil.FailErr(t, "pin large source", err)
	var payload []byte
	var logical int64
	testutil.FailErr(t, "read compressed snapshot", f.store.db.QueryRowContext(t.Context(), `SELECT payload,logical_bytes FROM editor_document_snapshots WHERE document_id=?`, d.ID).Scan(&payload, &logical))
	if int64(len(payload))*10 >= logical {
		t.Fatalf("snapshot compression: stored=%d logical=%d", len(payload), logical)
	}
	decoded, err := decodeSnapshot(payload)
	testutil.FailErr(t, "decode stored snapshot", err)
	if decoded.Draft != pinned.Draft || decoded.BaseContent != pinned.BaseContent || decoded.BaseSHA256 != pinned.BaseSHA256 || decoded.Revision != pinned.Revision || decoded.Epoch != pinned.Epoch || decoded.RootID != pinned.RootID {
		t.Fatal("snapshot payload changed durable state")
	}
	payload[len(payload)/2] ^= 0xff
	if _, err := decodeSnapshot(payload); err == nil {
		t.Fatal("corrupt snapshot was accepted")
	}
}

func TestSaveReplayAfterRetentionDoesNotPublishAgain(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "base"})
	d := f.open(t, "a.txt")
	operation := uuid.NewString()
	pin, err := f.service.PinSave(t.Context(), f.project.ID, d.ID, "window", operation)
	testutil.FailErr(t, "reserve save", err)
	_, err = f.service.Save(t.Context(), f.project, d.ID, "window", operation, "", 0, pin.Revision)
	testutil.FailErr(t, "complete save with lost acknowledgement", err)
	_, err = f.store.db.ExecContext(t.Context(), `UPDATE editor_mutations SET created_at='2000-01-01T00:00:00Z' WHERE id=?`, operation)
	testutil.FailErr(t, "age completed response", err)
	_, err = db.RunRetention(t.Context(), f.store.db, db.DefaultRetention())
	testutil.FailErr(t, "compact old response", err)
	receipt, err := f.store.Mutation(t.Context(), operation)
	testutil.FailErr(t, "read durable operation receipt", err)
	if !receipt.ReplayCompacted || receipt.ResponseJSON != "" {
		t.Fatal("old save kept its full response or lost its identity")
	}
	current, err := f.service.CurrentSnapshot(t.Context(), f.project.ID, d.ID)
	testutil.FailErr(t, "read current document", err)
	current, err = f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: current.Revision}, Content: "new unsaved work", EOL: "lf"})
	testutil.FailErr(t, "type new work", err)
	resumedPin, err := f.service.PinSave(t.Context(), f.project.ID, d.ID, "window", operation)
	testutil.FailErr(t, "resume an older local save phase", err)
	if resumedPin.SaveRevision != pin.Revision || resumedPin.Draft != current.Draft {
		t.Fatal("pin replay lost its original save revision or current draft")
	}
	replay, err := f.service.Save(t.Context(), f.project, d.ID, "window", operation, "", 0, pin.Revision)
	testutil.FailErr(t, "reconnect after response retention", err)
	if replay.Draft != current.Draft || !replay.Dirty || f.disk(t, "a.txt") != "base" {
		t.Fatalf("old operation published or lost newer work: %+v", replay)
	}
	_, err = f.service.Save(t.Context(), f.project, d.ID, "window", operation, "", 0, pin.Revision+1)
	if !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("compaction forgot input identity: %v", err)
	}
}

// A reservation taken before the saved base moved on is stale: the save is
// refused, its reserved bytes are released, and a delayed replay of the same
// operation stays refused without publishing anything.
func TestRejectedSaveReleasesReservationWithoutPublishingOnDelayedReplay(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "base\n"})
	d := f.open(t, "a.txt")
	d, err := f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision}, Content: "base\npending draft\n", EOL: "lf"})
	testutil.FailErr(t, "type draft", err)
	operation := uuid.NewString()
	pin, err := f.service.PinSave(t.Context(), f.project.ID, d.ID, "window", operation)
	testutil.FailErr(t, "reserve save", err)
	// The saved base moves on after the save was reserved.
	f.write(t, "a.txt", "moved\n")
	moved, err := f.service.ObserveDisk(t.Context(), f.project, d.ID, "window")
	testutil.FailErr(t, "import the outside change", err)
	if moved.Draft != "moved\npending draft\n" {
		t.Fatalf("import = %q", moved.Draft)
	}
	_, err = f.service.Save(t.Context(), f.project, d.ID, "window", operation, "", 0, pin.Revision)
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("save against a moved base = %v", err)
	}
	var count int
	testutil.FailErr(t, "count remaining reservations", f.store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM editor_save_pins`).Scan(&count))
	if count != 0 {
		t.Fatal("rejected save retained reserved snapshot bytes")
	}
	_, err = f.service.Save(t.Context(), f.project, d.ID, "window", operation, "", 0, pin.Revision)
	if !errors.Is(err, ErrRevisionConflict) || f.disk(t, "a.txt") != "moved\n" {
		t.Fatalf("rejected operation published on replay: %v", err)
	}
	current, err := f.store.Get(t.Context(), d.ID)
	testutil.FailErr(t, "read retained draft", err)
	if current.Draft != "moved\npending draft\n" || !current.Dirty || current.Diverged {
		t.Fatalf("rejection disturbed the user's draft: %+v", current)
	}
}
