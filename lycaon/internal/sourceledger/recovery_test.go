package sourceledger

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRecoveryReferenceProtectsCompleteObjectUntilRetired(t *testing.T) {
	store, ctx := openLedger(t)
	source := filepath.Join(t.TempDir(), "large.bin")
	body := bytes.Repeat([]byte{0, 1, 255, 42}, 2*1024*1024)
	testutil.FailErr(t, "seed", os.WriteFile(source, body, 0o600))
	scope, err := os.OpenRoot(filepath.Dir(source))
	testutil.FailErr(t, "open source root", err)
	defer func() { _ = scope.Close() }()
	writer, err := store.Retention.BeginRecovery(ctx, "p1", "recovery")
	testutil.FailErr(t, "begin recovery", err)
	defer writer.Close()
	saved, err := writer.Append(ctx, RecoveryEntry{Path: ".", Mode: 0o600}, scope, filepath.Base(source), nil)
	testutil.FailErr(t, "retain complete file", err)
	testutil.FailErr(t, "flush recovery", writer.Flush(ctx))
	writer.Close()
	testutil.FailErr(t, "remove original", os.Remove(source))
	_, err = store.sqlDB.ExecContext(ctx, `INSERT INTO source_mutations(id,project_id,kind,input_digest,plan_json,status,error,created_at,updated_at) VALUES('recovery','p1','delete','digest','{"recovery_id":"recovery"}','failed','','2026-01-01','2026-01-01')`)
	testutil.FailErr(t, "retain interrupted operation", err)
	testutil.FailErr(t, "sweep", store.Retention.SweepBlobs(ctx))
	var restored bytes.Buffer
	testutil.FailErr(t, "read recovery", store.Retention.CopyRecoveryFile(ctx, saved.SHA, &restored))
	if !bytes.Equal(restored.Bytes(), body) {
		t.Fatal("recovery content truncated or changed")
	}
	_, err = store.sqlDB.ExecContext(ctx, `DELETE FROM source_mutations WHERE id='recovery'`)
	testutil.FailErr(t, "retire recovery", err)
	testutil.FailErr(t, "reclaim", store.Retention.SweepBlobs(ctx))
	if err := store.Retention.CopyRecoveryFile(ctx, saved.SHA, &bytes.Buffer{}); err == nil {
		t.Fatal("retired bytes remain")
	}
}
