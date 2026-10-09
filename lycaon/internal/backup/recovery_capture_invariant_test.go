package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/editoroutbox"
	"github.com/lycaon/lycaon/internal/historyretention"
	"github.com/lycaon/lycaon/internal/localdata"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRecoveryCaptureFailureRemovesOnlyItsIncompleteDestination(t *testing.T) {
	for _, failure := range []string{"closed store", "canceled", "invalid outbox", "invalid durable file", "invalid durable directory"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			database := testdbfixture.OpenPath(t, filepath.Join(root, storeRelPath))
			opts := CreateOpts{ConfigDir: root, SQLDB: database, SchemaUserVersion: db.SchemaVersion}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch failure {
			case "closed store":
				testutil.FailErr(t, "close capture store", database.Close())
			case "canceled":
				cancel()
			case "invalid outbox":
				testutil.FailErr(t, "block outbox tree", os.WriteFile(filepath.Join(root, editoroutbox.Directory()), []byte("durable work"), 0o600))
			case "invalid durable file":
				testutil.FailErr(t, "replace file with directory", os.Mkdir(filepath.Join(root, historyretention.PolicyFilename), 0o700))
			case "invalid durable directory":
				for _, dir := range localdata.BackupRelDirs() {
					if dir != editoroutbox.Directory() {
						testutil.FailErr(t, "block durable directory", os.WriteFile(filepath.Join(root, dir), []byte("durable work"), 0o600))
						break
					}
				}
			}
			retained := filepath.Join(root, "previous-recovery")
			testutil.FailErr(t, "retain previous recovery", os.WriteFile(retained, []byte("previous bytes"), 0o600))
			destination := filepath.Join(root, "incomplete-capture")
			manifest, _, err := captureRecoveryDirectory(ctx, opts, destination)
			if err == nil || len(manifest.Files) != 0 {
				t.Fatal("failed capture returned a publishable manifest")
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatalf("incomplete snapshot survived failure: %v", err)
			}
			got, err := os.ReadFile(retained)
			testutil.FailErr(t, "read prior recovery", err)
			if string(got) != "previous bytes" {
				t.Fatal("failed capture changed prior recovery")
			}
		})
	}
}

func TestRecoveryMaterializationNeverOverwritesAnExistingDestination(t *testing.T) {
	root := t.TempDir()
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
	testutil.FailErr(t, "seed source", os.WriteFile(source, []byte("new bytes"), 0o600))
	testutil.FailErr(t, "seed retained destination", os.WriteFile(destination, []byte("retained bytes"), 0o600))
	if err := copySnapshotStream(t.Context(), source, destination, 0o600); !os.IsExist(err) {
		t.Fatalf("existing destination error = %v", err)
	}
	got, err := os.ReadFile(destination)
	testutil.FailErr(t, "read retained destination", err)
	if string(got) != "retained bytes" {
		t.Fatal("capture overwrote existing recovery work")
	}
	var usage RecoveryCaptureUsage
	if err := usage.addFile(filepath.Join(root, "missing"), false); !os.IsNotExist(err) || usage != (RecoveryCaptureUsage{}) {
		t.Fatalf("failed materialization contributed accounting: %+v, %v", usage, err)
	}
}
