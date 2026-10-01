//go:build stress

package db_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/editoroutbox"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Exercises retained source history and recovery over 250 simulated working days.
func TestStressEditorWorkingYearWithRecovery(t *testing.T) {
	config, root := t.TempDir(), t.TempDir()
	dbPath := filepath.Join(config, "store.db")
	database := testdbfixture.OpenPath(t, dbPath)
	rootID := uuid.NewString()
	testdbseed.InsertProjectRootWithID(t, database, testdbseed.DefaultProjectID, rootID, root)
	p := &project.Project{ID: testdbseed.DefaultProjectID, Roots: []project.Root{{ID: rootID, ProjectID: testdbseed.DefaultProjectID, Path: root, IsPrimary: true}}}
	service := editordoc.New(editordoc.NewStore(database), sourceledger.New(database, filepath.Join(config, "source-content")), storageStressRoots{p})
	t.Cleanup(func() { _ = service.Close(context.Background()) })
	random := rand.New(rand.NewPCG(31, 47))
	var body strings.Builder
	for body.Len() < 20<<10 {
		fmt.Fprintf(&body, "const v%x = %d;\n", random.Uint64(), random.Uint64())
	}
	documents := make([]*editordoc.Document, 10)
	for index := range documents {
		name := fmt.Sprintf("source-%d.ts", index)
		testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(root, name), []byte("// revision 000000\n"+body.String()), 0o600))
		var err error
		documents[index], err = service.Open(t.Context(), p, name, rootID, "", "window", nil)
		testutil.FailErr(t, "open source", err)
	}
	for day := 0; day < 250; day++ {
		for save := 0; save < 100; save++ {
			index := save % len(documents)
			current := documents[index]
			changed, err := service.ReplaceSnapshot(t.Context(), current.ID, p.ID, editordoc.SnapshotReplacement{
				DocumentCommand: editordoc.DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: current.Revision, HistoryVector: current.StateVector},
				Content:         fmt.Sprintf("// revision %06d\n", day*100+save+1) + body.String(), EOL: "lf",
			})
			testutil.FailErr(t, "edit source", err)
			operation := uuid.NewString()
			pinned, err := service.PinSave(t.Context(), p.ID, changed.ID, "window", operation)
			testutil.FailErr(t, "reserve save", err)
			documents[index], err = service.Save(t.Context(), p, changed.ID, "window", operation, "", 0, pinned.Revision)
			testutil.FailErr(t, "publish source", err)
			// Receipt timestamps track the simulated working year.
			_, err = database.ExecContext(t.Context(), `UPDATE editor_mutations SET updated_at=? WHERE id=?`, time.Now().UTC().Add(-time.Duration(249-day)*24*time.Hour).Format(time.RFC3339Nano), operation)
			testutil.FailErr(t, "date publication receipt", err)
		}
		_, err := db.RunRetention(t.Context(), database, db.DefaultRetention())
		testutil.FailErr(t, "maintain completed history", err)
		if (day+1)%50 == 0 {
			t.Logf("completed %d working days / %d real saves", day+1, (day+1)*100)
		}
	}
	for _, document := range documents {
		pinned, err := service.Pin(t.Context(), p.ID, document.ID, document.Revision)
		testutil.FailErr(t, "capture acknowledged editor state", err)
		preserveYearCheckpoint(t, config, pinned)
	}
	testutil.FailErr(t, "validate native editor envelopes", editoroutbox.Validate(t.Context(), config))
	testutil.FailErr(t, "close editor before update", service.Close(t.Context()))
	live := yearFileUsage(t, config)
	plan, err := db.PlanUpgrade(t.Context(), database)
	testutil.FailErr(t, "plan baseline", err)
	for update := 1; update <= 3; update++ {
		version := fmt.Sprintf("1.0.%d", update)
		testutil.FailErr(t, "capture independent recovery", backup.CaptureUpgradeRecovery(t.Context(), backup.CreateOpts{ConfigDir: config, DBPath: dbPath, SQLDB: database, SchemaUserVersion: db.SchemaVersion, AppVersion: fmt.Sprintf("1.0.%d", update-1)}, plan, version))
		if update < 3 {
			testutil.FailErr(t, "complete preceding update", backup.CompleteUpgradeRecovery(config, version))
		}
	}
	recoveries, err := os.ReadDir(filepath.Join(config, db.UpgradeRecoveryDirName))
	testutil.FailErr(t, "inspect retained recovery points", err)
	points := 0
	for _, entry := range recoveries {
		if entry.IsDir() {
			points++
		}
	}
	if points != 3 {
		t.Fatalf("recovery peak retained %d points, want two completed and one pending", points)
	}
	peak := yearFileUsage(t, config)
	var saves, versions, snapshots int64
	testutil.FailErr(t, "count save identities", database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM editor_mutations`).Scan(&saves))
	testutil.FailErr(t, "count retained versions", database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM source_versions WHERE capture_state='stored' AND landing='working_file'`).Scan(&versions))
	testutil.FailErr(t, "count snapshot cache", database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM editor_document_snapshots`).Scan(&snapshots))
	if saves != 25000 || versions < 25000 || max(peak.logical, peak.allocated) > 8<<30 {
		t.Fatalf("annual workload: saves=%d versions=%d peak=%+v", saves, versions, peak)
	}
	t.Logf("250 days x 100 real saves, ten %d-byte sources: live logical/allocated bytes %+v; live plus three recovery captures %+v; source bodies %+v; native editor %+v; save identities %d; source versions %d; cached snapshots %d", len("// revision 000000\n")+body.Len(), live, peak, yearFileUsage(t, filepath.Join(config, "source-content")), yearFileUsage(t, filepath.Join(config, editoroutbox.Directory())), saves, versions, snapshots)
}

func preserveYearCheckpoint(t *testing.T, root string, document *editordoc.Document) {
	t.Helper()
	address := map[string]string{"documentId": document.ID, "projectId": document.ProjectID, "rootId": document.RootID, "fileId": document.FileID, "path": document.Path}
	checkpoint := map[string]any{"kind": "checkpoint", "clientId": "window", "state": document.CRDTUpdate, "epoch": document.Epoch, "synchronized": true, "pendingOperations": []string{}}
	for key, value := range address {
		checkpoint[key] = value
	}
	record, err := json.Marshal(checkpoint)
	testutil.FailErr(t, "encode native checkpoint", err)
	record = append(record, '\n')
	sum := sha256.Sum256(record)
	name := sha256.Sum256([]byte("window\x00checkpoint\x00checkpoint"))
	frame := map[string]any{"name": hex.EncodeToString(name[:]), "kind": "checkpoint", "offset": 0, "length": len(record), "sha256": hex.EncodeToString(sum[:]), "synchronized": true}
	header, err := json.Marshal(map[string]any{"format": 1, "address": address, "updatedAt": time.Now().Unix(), "synchronized": true, "log": "records-1.log", "logBytes": len(record), "frames": []map[string]any{frame}})
	testutil.FailErr(t, "encode native header", err)
	identity := sha256.Sum256([]byte(document.ID))
	directory := filepath.Join(root, editoroutbox.Directory(), hex.EncodeToString(identity[:]))
	testutil.FailErr(t, "create native directory", os.MkdirAll(directory, 0o700))
	testutil.FailErr(t, "preserve native checkpoint", os.WriteFile(filepath.Join(directory, "records-1.log"), record, 0o600))
	testutil.FailErr(t, "publish native transaction", os.WriteFile(filepath.Join(directory, "header.json"), header, 0o600))
}

type yearDiskUsage struct{ logical, allocated int64 }

// Allocated bytes include block rounding and may double-count shared clone extents.
func yearFileUsage(t *testing.T, root string) yearDiskUsage {
	t.Helper()
	var bytes yearDiskUsage
	testutil.FailErr(t, "measure installation files", filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			bytes.logical += info.Size()
			bytes.allocated += yearAllocatedBytes(info)
		}
		return nil
	}))
	return bytes
}
