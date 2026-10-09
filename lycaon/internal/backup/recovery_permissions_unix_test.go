//go:build !windows

package backup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/editoroutbox"
	"github.com/lycaon/lycaon/internal/historyretention"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRecoveryCaptureDoesNotPublishWithoutReadableDurablePayloads(t *testing.T) {
	for _, relative := range []string{historyretention.PolicyFilename, filepath.Join(editoroutbox.Directory(), "document", "records.log")} {
		t.Run(relative, func(t *testing.T) {
			root := t.TempDir()
			database := testdbfixture.OpenPath(t, filepath.Join(root, storeRelPath))
			payload := filepath.Join(root, relative)
			testutil.FailErr(t, "create durable parent", os.MkdirAll(filepath.Dir(payload), 0o700))
			testutil.FailErr(t, "seed unreadable durable payload", os.WriteFile(payload, []byte("retained user work"), 0o000))
			t.Cleanup(func() { _ = os.Chmod(payload, 0o600) })
			destination := filepath.Join(root, "snapshot")
			_, _, err := captureRecoveryDirectory(t.Context(), CreateOpts{ConfigDir: root, SQLDB: database, SchemaUserVersion: db.SchemaVersion}, destination)
			if os.Geteuid() == 0 {
				testutil.FailErr(t, "privileged process can read the retained payload", err)
			} else {
				if !os.IsPermission(err) {
					t.Fatalf("unreadable payload capture error = %v", err)
				}
				if _, err := os.Stat(destination); !os.IsNotExist(err) {
					t.Fatalf("unreadable payload left a publishable recovery: %v", err)
				}
			}
			testutil.FailErr(t, "restore original permissions", os.Chmod(payload, 0o600))
			got, err := os.ReadFile(payload)
			testutil.FailErr(t, "read retained original work", err)
			if string(got) != "retained user work" {
				t.Fatal("failed capture changed the retained payload")
			}
		})
	}
}
