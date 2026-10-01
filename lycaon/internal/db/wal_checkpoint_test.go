package db

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCheckpointWALCopiesFramesWithoutTruncatingLiveLog(t *testing.T) {
	sqlDB, dbPath := openTestDBWithPath(t)
	growWAL(t, sqlDB, "grow", 200)

	if size := walSize(t, dbPath); size == 0 {
		t.Fatal("wal did not grow; the test cannot show a reclaim that did not have to happen")
	}

	result, err := CheckpointWAL(t.Context(), sqlDB)
	testutil.FailErr(t, "CheckpointWAL", err)

	if result.Mode != WALCheckpointPassive || !result.Complete() {
		t.Fatalf("passive checkpoint = mode:%s busy:%v log:%d checkpointed:%d",
			result.Mode, result.Busy, result.LogFrames, result.CheckpointedFrames)
	}
	if size := walSize(t, dbPath); size == 0 {
		t.Fatal("live passive checkpoint truncated the WAL")
	}
}

func TestCheckpointWALReportsPinnedLog(t *testing.T) {
	sqlDB, dbPath := openTestDBWithPath(t)

	conn, err := sqlDB.reader.Conn(t.Context())
	testutil.FailErr(t, "Conn", err)
	defer func() { _ = conn.Close() }()
	_, err = conn.ExecContext(t.Context(), `BEGIN`)
	testutil.FailErr(t, "BEGIN reader", err)
	var seen int
	testutil.FailErr(t, "reader snapshot",
		conn.QueryRowContext(t.Context(), `SELECT count(*) FROM code_scans`).Scan(&seen))

	growWAL(t, sqlDB, "pinned", 200)

	result, err := CheckpointWAL(t.Context(), sqlDB)
	testutil.FailErr(t, "CheckpointWAL", err)

	if result.Complete() || result.PinnedFrames() == 0 {
		t.Fatalf("pinned checkpoint = busy:%v log:%d checkpointed:%d",
			result.Busy, result.LogFrames, result.CheckpointedFrames)
	}
	if walSize(t, dbPath) == 0 {
		t.Fatal("wal file is empty while a reader holds a snapshot; the pin was not real")
	}

	_, err = conn.ExecContext(t.Context(), `ROLLBACK`)
	testutil.FailErr(t, "ROLLBACK reader", err)

	after, err := CheckpointWAL(t.Context(), sqlDB)
	testutil.FailErr(t, "CheckpointWAL after release", err)
	if !after.Complete() {
		t.Fatalf("checkpoint incomplete after reader released: busy=%v log=%d checkpointed=%d",
			after.Busy, after.LogFrames, after.CheckpointedFrames)
	}
}

func TestCheckpointWALRejectsNilDatabase(t *testing.T) {
	if _, err := CheckpointWAL(t.Context(), nil); err == nil {
		t.Fatal("CheckpointWAL(nil) = nil error, want a rejection")
	}
}

func TestStoreShutdownTruncatesWALAfterReadersDrain(t *testing.T) {
	sqlDB, dbPath := openTestDBWithPath(t)
	growWAL(t, sqlDB, "shutdown", 200)
	if size := walSize(t, dbPath); size == 0 {
		t.Fatal("wal did not grow")
	}
	testutil.FailErr(t, "close store", sqlDB.Close())
	if size := walSize(t, dbPath); size != 0 {
		t.Fatalf("wal file after shutdown = %d bytes, want 0", size)
	}
}

func openTestDBWithPath(t *testing.T) (*Store, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "store.db")
	sqlDB, err := Open(dbPath)
	testutil.FailErr(t, "Open", err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return sqlDB, dbPath
}

// growWAL writes rows wide enough that the log is unmistakably non-empty.
func growWAL(t *testing.T, sqlDB Handle, prefix string, rows int) {
	t.Helper()
	payload := strings.Repeat("x", 4096)
	for i := range rows {
		_, err := sqlDB.ExecContext(t.Context(), `
			INSERT INTO code_scans (
				id, canonical_path, categories_json, status, result_json, created_at, trigger, reuse_key
			) VALUES (?, '/tmp/project', '["sast"]', 'complete', ?, '2026-01-01T00:00:00Z', 'project_open', ?)
		`, prefix+"-"+strconv.Itoa(i), strconv.Quote(payload), prefix+"-"+strconv.Itoa(i))
		testutil.FailErr(t, "insert wal row", err)
	}
}

func walSize(t *testing.T, dbPath string) int64 {
	t.Helper()
	info, err := os.Stat(dbPath + "-wal")
	if os.IsNotExist(err) {
		return 0
	}
	testutil.FailErr(t, "stat wal", err)
	return info.Size()
}
