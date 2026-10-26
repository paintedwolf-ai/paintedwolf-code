package sourceledger

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRecoveryWalkReleasesCursorBeforeCallbacksAcrossPages(t *testing.T) {
	store, parent := openLedger(t)
	database := store.sqlDB.(*db.Store)
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	writer, err := store.Retention.BeginRecovery(ctx, "p1", "paged")
	testutil.FailErr(t, "begin recovery", err)
	defer writer.Close()
	const count = recoveryPageSize + 3
	for i := 0; i < count; i++ {
		_, err := writer.Append(ctx, RecoveryEntry{Path: fmt.Sprint(i), Mode: uint32(os.ModeDir | 0o700)}, nil, "", nil)
		testutil.FailErr(t, "append directory", err)
	}
	testutil.FailErr(t, "flush manifest", writer.Flush(ctx))
	for _, reverse := range []bool{false, true} {
		seen := 0
		err := store.Retention.WalkRecovery(ctx, "p1", "paged", count, reverse, func(entry RecoveryEntry) error {
			if active := database.Stats().Reader.InUse; active != 0 {
				return fmt.Errorf("recovery callback retains %d reader connections", active)
			}
			want := seen
			if reverse {
				want = count - 1 - seen
			}
			if entry.Path != fmt.Sprint(want) {
				return fmt.Errorf("entry %d: got %s, want %d", seen, entry.Path, want)
			}
			seen++
			_, err := store.sqlDB.ExecContext(ctx, `UPDATE source_recovery_entries SET mode=mode WHERE project_id=? AND recovery_id=? AND path=?`, "p1", "paged", entry.Path)
			return err
		})
		testutil.FailErr(t, "walk while writing after cursor release", err)
		if seen != count {
			t.Fatalf("visited %d entries, want %d", seen, count)
		}
	}
	interrupted := errors.New("stop recovery")
	if err := store.Retention.WalkRecovery(ctx, "p1", "paged", count, false, func(RecoveryEntry) error { return interrupted }); !errors.Is(err, interrupted) {
		t.Fatalf("callback failure lost: %v", err)
	}
	if active := database.Stats().Reader.InUse; active != 0 {
		t.Fatalf("interrupted walk retains %d reader connections", active)
	}
	if err := store.Retention.WalkRecovery(ctx, "p1", "paged", count+1, false, func(RecoveryEntry) error { return nil }); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("incomplete manifest accepted: %v", err)
	}
}

func TestRecoveryWriterDeduplicatesRepeatedObjectHashesAcrossPages(t *testing.T) {
	store, ctx := openLedger(t)
	dir := t.TempDir()
	testutil.FailErr(t, "write a", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("aaa"), 0o600))
	testutil.FailErr(t, "write b", os.WriteFile(filepath.Join(dir, "b.txt"), []byte("bbb"), 0o600))
	scope, err := os.OpenRoot(dir)
	testutil.FailErr(t, "open root", err)
	defer func() { _ = scope.Close() }()

	writer, err := store.Retention.BeginRecovery(ctx, "p1", "dedup")
	testutil.FailErr(t, "begin recovery", err)
	defer writer.Close()

	// Append two entries with identical content and one with distinct content:
	// the current page must queue only the two unique objects.
	first, err := writer.Append(ctx, RecoveryEntry{Path: "f0", Mode: 0o600}, scope, "a.txt", nil)
	testutil.FailErr(t, "append f0", err)
	_, err = writer.Append(ctx, RecoveryEntry{Path: "f1", Mode: 0o600}, scope, "a.txt", nil)
	testutil.FailErr(t, "append f1", err)
	second, err := writer.Append(ctx, RecoveryEntry{Path: "f2", Mode: 0o600}, scope, "b.txt", nil)
	testutil.FailErr(t, "append f2", err)
	if len(writer.objects) != 2 {
		t.Fatalf("page queued %d objects, want 2 unique captures", len(writer.objects))
	}

	// Fill the rest of the first page and spill into a second page using the
	// already-seen content hashes.
	const total = recoveryPageSize + 10
	for i := 3; i < total; i++ {
		src := "a.txt"
		if i%2 == 1 {
			src = "b.txt"
		}
		_, err := writer.Append(ctx, RecoveryEntry{Path: fmt.Sprintf("f%d", i), Mode: 0o600}, scope, src, nil)
		testutil.FailErr(t, "append repeated", err)
	}
	if len(writer.objects) != 0 {
		t.Fatalf("second page queued %d already-committed objects, want 0", len(writer.objects))
	}

	// Exercise the bounded committedObjects reset when full.
	for i := range recoverySeenObjectMax {
		writer.committedObjects[fmt.Sprintf("seed-%d", i)] = struct{}{}
	}
	testutil.FailErr(t, "write c", os.WriteFile(filepath.Join(dir, "c.txt"), []byte("ccc"), 0o600))
	third, err := writer.Append(ctx, RecoveryEntry{Path: fmt.Sprintf("f%d", total), Mode: 0o600}, scope, "c.txt", nil)
	testutil.FailErr(t, "append f_last", err)
	testutil.FailErr(t, "flush final", writer.Flush(ctx))
	if len(writer.committedObjects) != 1 {
		t.Fatalf("committedObjects cap did not reset when full: len=%d", len(writer.committedObjects))
	}

	var objectRows, entryRows int
	testutil.FailErr(t, "count recovery objects", store.sqlDB.QueryRowContext(ctx,
		`SELECT count(*) FROM source_recovery_objects WHERE project_id='p1' AND recovery_id='dedup'`).Scan(&objectRows))
	testutil.FailErr(t, "count recovery entries", store.sqlDB.QueryRowContext(ctx,
		`SELECT count(*) FROM source_recovery_entries WHERE project_id='p1' AND recovery_id='dedup'`).Scan(&entryRows))
	if objectRows != 3 || entryRows != total+1 {
		t.Fatalf("recovery rows objects=%d entries=%d, want 3 and %d", objectRows, entryRows, total+1)
	}
	if first.SHA == "" || second.SHA == "" || third.SHA == "" || first.SHA == second.SHA {
		t.Fatalf("unexpected entry hashes: %q %q %q", first.SHA, second.SHA, third.SHA)
	}
}
