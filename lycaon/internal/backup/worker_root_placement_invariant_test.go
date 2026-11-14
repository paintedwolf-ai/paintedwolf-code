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
	for _, location := range []string{"current", "ancestor"} {
		t.Run(location, func(t *testing.T) {
			verifyRootHistoryPlacement(t, location)
		})
	}
}

func verifyRootHistoryPlacement(t *testing.T, location string) {
	t.Helper()
	fx := setupCheckpointBackupFixture(t)
	secondary := t.TempDir()
	testutil.FailErr(t, "seed secondary history", os.WriteFile(filepath.Join(secondary, "secondary.txt"), []byte("independent secondary history\n"), 0o600))
	gittest.InitCommit(t, secondary, "secondary history")
	gittest.Run(t, secondary, "repack", "-a", "-d")
	oid := strings.TrimSpace(gittest.Run(t, secondary, "rev-parse", "HEAD"))
	pg := checkpoint.PrivateGitSnapshot{
		HeadRef: "refs/heads/main", Refs: map[string]string{"refs/heads/main": fx.commitOID},
		RetainedObjects: []string{fx.commitOID, oid},
		Roots:           map[string]checkpoint.RootGitRefs{"secondary": {HeadRef: "refs/heads/main", Refs: map[string]string{"refs/heads/main": oid}}},
	}
	objectCheckpoint := "cp-1"
	if location == "ancestor" {
		objectCheckpoint = "cp-parent"
		writeNamedCheckpointManifest(t, fx.storeDir, objectCheckpoint, nil, fx.baseSHA, fx.finalSHA, checkpoint.PrivateGitSnapshot{})
		pg.ObjectCheckpoints = []string{objectCheckpoint}
	}
	writeCheckpointManifestAndMeta(t, fx, pg)
	if location == "ancestor" {
		writeNamedCheckpointManifest(t, fx.storeDir, "cp-1", []string{objectCheckpoint}, fx.baseSHA, fx.finalSHA, pg)
	}
	wrong := checkpoint.CheckpointGitObjectsDir(fx.storeDir, objectCheckpoint, "")
	correct := checkpoint.CheckpointGitObjectsDir(fx.storeDir, objectCheckpoint, "secondary")
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
	testutil.FailErr(t, "close original database", fx.database.Close())
	testutil.FailErr(t, "remove original installation", os.RemoveAll(fx.root))
	restored := t.TempDir()
	_, err = backup.Stage(t.Context(), backup.StageOpts{ConfigDir: restored, ArchivePath: archive, SchemaVersion: db.SchemaVersion})
	testutil.FailErr(t, "stage independent root history", err)
	testutil.FailErr(t, "apply independent root history", backup.ApplyPending(t.Context(), restored))
	reader := t.TempDir()
	gittest.Run(t, reader, "init", "--bare", "-q")
	restoredStore := filepath.Join(restored, "worker-recovery", "checkpoints")
	copyGitObjectsDir(t, checkpoint.CheckpointGitObjectsDir(restoredStore, objectCheckpoint, "secondary"), filepath.Join(reader, "objects"))
	if got := gittest.Run(t, reader, "cat-file", "-p", oid+":secondary.txt"); got != "independent secondary history\n" {
		t.Fatalf("restored isolated root content = %q", got)
	}
}
