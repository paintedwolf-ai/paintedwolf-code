//go:build stress

package db_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Released snapshots exercise production eviction over a simulated working year.
func TestStressEditorSnapshotWorkingYear(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	root := t.TempDir()
	rootID := uuid.NewString()
	testdbseed.InsertProjectRootWithID(t, sqlDB, testdbseed.DefaultProjectID, rootID, root)
	random := rand.New(rand.NewPCG(17, 29))
	var source strings.Builder
	for source.Len() < 128<<10 {
		fmt.Fprintf(&source, "const v%x = %d;\n", random.Uint64(), random.Uint64())
	}
	p := &project.Project{ID: testdbseed.DefaultProjectID, Roots: []project.Root{{ID: rootID, ProjectID: testdbseed.DefaultProjectID, Path: root, IsPrimary: true}}}
	service := editordoc.New(editordoc.NewStore(sqlDB), sourceledger.New(sqlDB, ""), storageStressRoots{p})
	t.Cleanup(func() { _ = service.Close(context.Background()) })
	type sourceSnapshot struct {
		document *editordoc.Document
		payload  []byte
		logical  int64
	}
	snapshots := make([]sourceSnapshot, 10)
	for index := range snapshots {
		name := fmt.Sprintf("file-%d.ts", index)
		testutil.FailErr(t, "write source file", os.WriteFile(filepath.Join(root, name), []byte(source.String()), 0o600))
		document, err := service.Open(t.Context(), p, name, rootID, "", "window", nil)
		testutil.FailErr(t, "open source", err)
		_, err = service.Pin(t.Context(), p.ID, document.ID, document.Revision)
		testutil.FailErr(t, "pin initial snapshot", err)
		snapshots[index].document = document
		testutil.FailErr(t, "read production encoding", sqlDB.QueryRowContext(t.Context(), `SELECT payload,logical_bytes FROM editor_document_snapshots WHERE document_id=?`, document.ID).Scan(&snapshots[index].payload, &snapshots[index].logical))
	}
	var peak int64
	for day := 0; day < 250; day++ {
		tx, err := sqlDB.BeginTx(t.Context(), nil)
		testutil.FailErr(t, "begin daily saves", err)
		for save := 0; save < 100; save++ {
			snapshot := snapshots[save%len(snapshots)]
			_, err = tx.ExecContext(t.Context(), `INSERT INTO editor_document_snapshots(document_id,revision,payload,logical_bytes,created_at) VALUES(?,?,?,?,?)`, snapshot.document.ID, day*100+save+2, snapshot.payload, snapshot.logical, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(day)*24*time.Hour+time.Duration(save)*time.Minute).Format(time.RFC3339Nano))
			testutil.FailErr(t, "retain released save snapshot", err)
		}
		testutil.FailErr(t, "commit daily snapshots", tx.Commit())
		_, err = service.Pin(t.Context(), p.ID, snapshots[0].document.ID, snapshots[0].document.Revision)
		testutil.FailErr(t, "automatic snapshot maintenance", err)
		var size, count int64
		testutil.FailErr(t, "measure retained cache", sqlDB.QueryRowContext(t.Context(), `SELECT COALESCE(SUM(length(payload)),0),COUNT(*) FROM editor_document_snapshots`).Scan(&size, &count))
		if size > 128<<20 || count > 4096 {
			t.Fatalf("day %d: cache grew to %d bytes / %d snapshots", day, size, count)
		}
		if size > peak {
			peak = size
		}
	}
	destination := filepath.Join(t.TempDir(), "recovery.db")
	testutil.FailErr(t, "capture compact recovery database", db.CreateSnapshot(t.Context(), sqlDB, destination))
	info, err := os.Stat(destination)
	testutil.FailErr(t, "measure recovery database", err)
	if info.Size() > 192<<20 {
		t.Fatalf("annual snapshot churn bloated recovery to %d bytes", info.Size())
	}
	t.Logf("250 days x 100 released snapshots, %d-byte source: cache peak %d bytes, recovery DB %d bytes, four database copies %d bytes", source.Len(), peak, info.Size(), 4*info.Size())
}

type storageStressRoots struct{ project *project.Project }

func (r storageStressRoots) Get(context.Context, string) (*project.Project, error) {
	return r.project, nil
}
