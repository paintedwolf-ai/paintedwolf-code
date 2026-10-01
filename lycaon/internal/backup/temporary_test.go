package backup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestInterruptedTransferCleanupPreservesRecoveryAndDoesNotFollowLinks(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	testutil.FailErr(t, "seed outside", os.WriteFile(filepath.Join(outside, "keep"), []byte("history"), 0o600))
	for _, name := range []string{".backup-export-123.zip", ".restore-upload-456.zip", "restore.pending.json", "my-backup.zip"} {
		testutil.FailErr(t, "seed file", os.WriteFile(filepath.Join(root, name), []byte("fixture"), 0o600))
	}
	for _, name := range []string{".restore-staging-123", ".restore-recovery-123", db.UpgradeRecoveryDirName} {
		testutil.FailErr(t, "seed recovery directory", os.Mkdir(filepath.Join(root, name), 0o700))
	}
	testutil.FailErr(t, "seed stale snapshot link", os.Symlink(outside, filepath.Join(root, ".backup-snapshot-123")))
	testutil.FailErr(t, "cleanup", CleanupInterruptedTransfers(root))
	for _, name := range []string{".backup-export-123.zip", ".restore-upload-456.zip", ".backup-snapshot-123"} {
		if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("temporary path %s remains: %v", name, err)
		}
	}
	for _, name := range []string{"restore.pending.json", "my-backup.zip", ".restore-staging-123", ".restore-recovery-123", db.UpgradeRecoveryDirName} {
		_, err := os.Stat(filepath.Join(root, name))
		testutil.FailErr(t, "preserve retained recovery", err)
	}
	_, err := os.Stat(filepath.Join(outside, "keep"))
	testutil.FailErr(t, "preserve symlink destination", err)
}
