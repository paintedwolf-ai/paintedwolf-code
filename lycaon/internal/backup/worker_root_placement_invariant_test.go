package backup_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func TestBackupRequiresGitHistoryInItsRecoverableRoot(t *testing.T) {
	fx := setupCheckpointBackupFixture(t)
	secondary := t.TempDir()
	testutil.FailErr(t, "seed secondary history", os.WriteFile(filepath.Join(secondary, "secondary.txt"), []byte("independent secondary history\n"), 0o600))
	gittest.InitCommit(t, secondary, "secondary history")
	gittest.Run(t, secondary, "repack", "-a", "-d")
	oid := strings.TrimSpace(gittest.Run(t, secondary, "rev-parse", "HEAD"))
	writeCheckpointManifestAndMeta(t, fx, checkpoint.PrivateGitSnapshot{
		HeadRef: "refs/heads/main", Refs: map[string]string{"refs/heads/main": fx.commitOID},
		RetainedObjects: []string{fx.commitOID, oid},
		Roots:           map[string]checkpoint.RootGitRefs{"secondary": {HeadRef: "refs/heads/main", Refs: map[string]string{"refs/heads/main": oid}}},
	})
	wrong := checkpoint.CheckpointGitObjectsDir(fx.storeDir, "cp-1", "")
	correct := checkpoint.CheckpointGitObjectsDir(fx.storeDir, "cp-1", "secondary")
	copyGitObjectsDir(t, filepath.Join(secondary, ".git", "objects"), wrong)
	opts := backup.CreateOpts{ConfigDir: fx.root, DBPath: filepath.Join(fx.root, "store.db"), SQLDB: fx.database, AppVersion: "test", SchemaUserVersion: db.SchemaVersion}
	if _, err := backup.Create(t.Context(), opts, filepath.Join(t.TempDir(), "wrong-root.zip")); !errors.Is(err, backup.ErrInvalid) {
		t.Fatalf("backup accepted history only recoverable by another root: %v", err)
	}
	// The bytes and required refs stay identical; only their root placement changes.
	copyGitObjectsDir(t, wrong, correct)
	testutil.FailErr(t, "remove misplaced copy", os.RemoveAll(wrong))
	testutil.FailErr(t, "remove original repository", os.RemoveAll(secondary))
	archive := filepath.Join(t.TempDir(), "correct-root.zip")
	_, err := backup.Create(t.Context(), opts, archive)
	testutil.FailErr(t, "create recoverable backup", err)
	_, err = backup.Stage(t.Context(), backup.StageOpts{ConfigDir: t.TempDir(), ArchivePath: archive, SchemaVersion: db.SchemaVersion})
	testutil.FailErr(t, "stage independent root history", err)
}
