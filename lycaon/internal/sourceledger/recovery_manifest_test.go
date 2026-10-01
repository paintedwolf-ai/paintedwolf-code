package sourceledger

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	writer, err := store.BeginRecovery(ctx, "p1", "paged")
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
		err := store.WalkRecovery(ctx, "p1", "paged", count, reverse, func(entry RecoveryEntry) error {
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
	if err := store.WalkRecovery(ctx, "p1", "paged", count, false, func(RecoveryEntry) error { return interrupted }); !errors.Is(err, interrupted) {
		t.Fatalf("callback failure lost: %v", err)
	}
	if active := database.Stats().Reader.InUse; active != 0 {
		t.Fatalf("interrupted walk retains %d reader connections", active)
	}
	if err := store.WalkRecovery(ctx, "p1", "paged", count+1, false, func(RecoveryEntry) error { return nil }); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("incomplete manifest accepted: %v", err)
	}
}
