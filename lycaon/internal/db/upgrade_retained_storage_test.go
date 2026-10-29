package db

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func TestRetainedStoragePlanMeasuresOnceAndAdmitsPublishedWAL(t *testing.T) {
	path, _ := seedLegacyManifestStore(t, t.TempDir(), map[string]int{"/work/a": 300, "/work/b": 37})
	reader, err := openReader(t.Context(), path)
	testutil.FailErr(t, "open released source", err)
	plan, err := PlanUpgrade(t.Context(), reader)
	testutil.FailErr(t, "plan released route", err)
	measured, err := plan.PrepareRetainedStorage(t.Context(), reader)
	testutil.FailErr(t, "measure unique source pages", err)
	if !measured.BoundedRetainedStorage() {
		t.Fatal("reviewed source did not receive bounded admission")
	}
	if measured.ScratchBytes() <= 64<<20 {
		t.Fatalf("missing source sort bound: %d", measured.ScratchBytes())
	}
	again, err := measured.PrepareRetainedStorage(t.Context(), nil)
	if err != nil || again.ScratchBytes() != measured.ScratchBytes() {
		t.Fatalf("repeated measurement: %d %v", again.ScratchBytes(), err)
	}
	testutil.FailErr(t, "close source", reader.Close())
	testutil.FailErr(t, "upgrade released store", UpgradeStaged(t.Context(), path))
	writer, err := openWriter(t.Context(), path, StoreOptions{})
	testutil.FailErr(t, "open published normal writer", err)
	defer writer.Close()
	var mode string
	testutil.FailErr(t, "read normal journal mode", writer.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&mode))
	if mode != "wal" {
		t.Fatalf("published mode=%s", mode)
	}
}

func TestPrivatePageLimitDiscardsFullWorkWithoutChangingRecovery(t *testing.T) {
	path, registry, plan := upgradeFixture(t)
	testutil.FailErr(t, "seal original", sealRebuiltStore(t.Context(), path))
	before, err := os.ReadFile(path)
	testutil.FailErr(t, "read original", err)
	replacement := filepath.Join(t.TempDir(), "replacement.db")
	testutil.FailErr(t, "copy private work", os.WriteFile(replacement, before, 0600))
	recovery := filepath.Join(t.TempDir(), "recovery.db")
	testutil.FailErr(t, "capture recovery", os.WriteFile(recovery, before, 0600))
	writer, err := openRetainedUpgradeWriter(t.Context(), replacement)
	testutil.FailErr(t, "open private writer", err)
	var pages uint64
	testutil.FailErr(t, "read page count", writer.QueryRowContext(t.Context(), "PRAGMA page_count").Scan(&pages))
	restore, err := limitUpgradePages(t.Context(), writer, pages)
	testutil.FailErr(t, "cap private pages", err)
	_, err = writer.ExecContext(t.Context(), "INSERT INTO history VALUES('full',zeroblob(1048576))")
	var full *sqlite.Error
	if !errors.As(err, &full) || full.Code()&0xff != sqlite3.SQLITE_FULL {
		t.Fatalf("private work did not hit page cap: %v", err)
	}
	testutil.FailErr(t, "restore page cap", restore())
	testutil.FailErr(t, "close failed private work", writer.Close())
	testutil.FailErr(t, "discard unpublished image", os.Remove(replacement))
	for _, retained := range []string{path, recovery} {
		after, err := os.ReadFile(retained)
		testutil.FailErr(t, "read retained image", err)
		if !bytes.Equal(before, after) {
			t.Fatal("private failure changed retained image")
		}
		reader, err := sql.Open("sqlite", fileDSN(retained, "mode=ro"))
		testutil.FailErr(t, "open retained image", err)
		var body string
		err = reader.QueryRowContext(t.Context(), "SELECT body FROM history WHERE id='retained'").Scan(&body)
		testutil.FailErr(t, "read retained history", err)
		testutil.FailErr(t, "close retained image", reader.Close())
		if body != "the original text" {
			t.Fatalf("retained body=%q", body)
		}
	}
	testutil.FailErr(t, "retry independent source route", executeUpgrade(t.Context(), path, plan, nil, registry))
}

func TestRetainedStorageDoesNotTrustChunkEntryCounts(t *testing.T) {
	path, _ := seedLegacyManifestStore(t, t.TempDir(), map[string]int{"/work/a": 300})
	writer, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open released source", err)
	defer writer.Close()
	plan, err := PlanUpgrade(t.Context(), writer)
	testutil.FailErr(t, "plan released source", err)
	before, err := plan.PrepareRetainedStorage(t.Context(), writer)
	testutil.FailErr(t, "measure physical source", err)
	_, err = writer.ExecContext(t.Context(), "UPDATE source_manifest_chunks SET entry_count=1")
	testutil.FailErr(t, "make legacy counts inaccurate", err)
	after, err := plan.PrepareRetainedStorage(t.Context(), writer)
	testutil.FailErr(t, "measure physical source with wrong counts", err)
	if after.ScratchBytes() != before.ScratchBytes() {
		t.Fatalf("stored counts changed physical budget: %d -> %d", before.ScratchBytes(), after.ScratchBytes())
	}
}
