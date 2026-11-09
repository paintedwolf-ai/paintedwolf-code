package backup_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBackupValidatesPublishedWorkerCheckpointsAndGitObjects(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	root := testbaseline.DataDir(t, database)
	testdbseed.InsertProject(t, database, testdbseed.DefaultProjectID)

	branchRel := "worker-branches/0123456789abcdef/job-cp"
	_, err := database.ExecContext(t.Context(),
		`INSERT INTO worker_jobs(id,project_id,agent_type,prompt,brief,workspace_relpath,created_at) VALUES('job-cp',?,'implementer','work','work',?,'2026-01-01T00:00:00Z')`,
		testdbseed.DefaultProjectID, branchRel)
	testutil.FailErr(t, "insert checkpoint worker", err)

	storeDir := filepath.Join(root, "worker-recovery", "checkpoints")
	store, err := checkpoint.NewStore(storeDir)
	testutil.FailErr(t, "open checkpoint store", err)

	baseSHA, _, err := store.WriteBlobBytes([]byte("package before\n"))
	testutil.FailErr(t, "write base blob", err)
	finalSHA, finalPath, err := store.WriteBlobBytes([]byte("package after\n"))
	testutil.FailErr(t, "write final blob", err)

	gitOID := strings.Repeat("a", 40)
	gitLoosePath := filepath.Join(root, "worker-branches", ".adapter", "privategit", "retained-store", "objects", gitOID[:2], gitOID[2:])
	testutil.FailErr(t, "mkdir retained git object", os.MkdirAll(filepath.Dir(gitLoosePath), 0o700))
	testutil.FailErr(t, "write retained git object", os.WriteFile(gitLoosePath, []byte("git-object-bytes"), 0o444))

	cpManifest := checkpoint.Manifest{
		CheckpointID:  "cp-1",
		FormatVersion: checkpoint.FormatVersion,
		WorkerID:      "job-cp",
		TaskID:        "job-cp",
		ProjectID:     testdbseed.DefaultProjectID,
		Entries: []checkpoint.ManifestEntry{{
			Path:           layeredfs.QualifiedPath{RootID: "primary", Rel: "main.go"},
			Kind:           layeredfs.EntryRegular,
			ContentChanged: true,
			Final:          layeredfs.ChangeFinalAttr{Mode: 0o644, Size: int64(len("package after\n")), SHA256: finalSHA},
			Base:           layeredfs.BaseCapture{Kind: layeredfs.EntryRegular, Mode: 0o644, Size: int64(len("package before\n")), SHA256: baseSHA},
		}},
		PrivateGit: checkpoint.PrivateGitSnapshot{
			HeadRef:         "refs/heads/main",
			Refs:            map[string]string{"refs/heads/main": gitOID},
			RetainedObjects: []string{gitOID},
		},
	}
	cpBytes, err := json.MarshalIndent(cpManifest, "", "  ")
	testutil.FailErr(t, "marshal checkpoint manifest", err)
	cpManifestPath := filepath.Join(storeDir, "checkpoints", "cp-1", "manifest.json")
	testutil.FailErr(t, "mkdir checkpoint manifest", os.MkdirAll(filepath.Dir(cpManifestPath), 0o700))
	testutil.FailErr(t, "write checkpoint manifest", os.WriteFile(cpManifestPath, cpBytes, 0o400))

	metaPath := filepath.Join(root, filepath.FromSlash(branchRel+".meta/state.json"))
	testutil.FailErr(t, "mkdir branch meta", os.MkdirAll(filepath.Dir(metaPath), 0o700))
	metaJSON := []byte(`{"format_version":2,"roots":[{"id":"primary","path":"/tmp/project","label":"primary","is_primary":true}],"snapshot_complete":true,"last_published_checkpoint_id":"cp-1","checkpoint_store_dir":"` + filepath.ToSlash(storeDir) + `"}`)
	testutil.FailErr(t, "write branch meta", os.WriteFile(metaPath, metaJSON, 0o600))

	// Even with the branch working tree evicted on disk, Create succeeds because the checkpoint and retained Git objects are complete.
	opts := backup.CreateOpts{ConfigDir: root, DBPath: filepath.Join(root, "store.db"), SQLDB: database, AppVersion: "test", SchemaUserVersion: db.SchemaVersion}
	rawArchive, manifest, err := createArchive(t, t.Context(), opts)
	testutil.FailErr(t, "create archive for evicted checkpoint-backed worker", err)
	if slices.Contains(manifest.RetainedBranchTrees, branchRel) {
		t.Fatalf("sealed checkpoint-backed worker should not require a retained branch tree: %+v", manifest.RetainedBranchTrees)
	}

	// Omitting the checkpoint blob from the archive must be rejected by Stage.
	blobRel := "worker-recovery/checkpoints/blobs/" + finalSHA
	missingBlobZip := omitRetainedFile(t, rawArchive, manifest, blobRel)
	missingBlobPath := filepath.Join(t.TempDir(), "missing-blob.zip")
	testutil.FailErr(t, "write missing-blob archive", os.WriteFile(missingBlobPath, missingBlobZip, 0o600))
	if _, err := backup.Stage(t.Context(), backup.StageOpts{ConfigDir: t.TempDir(), ArchivePath: missingBlobPath, SchemaVersion: db.SchemaVersion}); !errors.Is(err, backup.ErrInvalid) {
		t.Fatalf("Stage with missing checkpoint blob err = %v, want ErrInvalid", err)
	}

	// Omitting the retained Git object from the archive must be rejected by Stage.
	gitRel := "worker-branches/.adapter/privategit/retained-store/objects/" + gitOID[:2] + "/" + gitOID[2:]
	missingGitZip := omitRetainedFile(t, rawArchive, manifest, gitRel)
	missingGitPath := filepath.Join(t.TempDir(), "missing-git.zip")
	testutil.FailErr(t, "write missing-git archive", os.WriteFile(missingGitPath, missingGitZip, 0o600))
	if _, err := backup.Stage(t.Context(), backup.StageOpts{ConfigDir: t.TempDir(), ArchivePath: missingGitPath, SchemaVersion: db.SchemaVersion}); !errors.Is(err, backup.ErrInvalid) {
		t.Fatalf("Stage with missing retained git object err = %v, want ErrInvalid", err)
	}

	// Removing the final blob on disk must cause backup.Create to fail with ErrInvalid.
	testutil.FailErr(t, "chmod final blob", os.Chmod(finalPath, 0o600))
	testutil.FailErr(t, "remove final blob", os.Remove(finalPath))
	if _, err := backup.Create(t.Context(), opts, filepath.Join(t.TempDir(), "broken.zip")); !errors.Is(err, backup.ErrInvalid) {
		t.Fatalf("Create with missing checkpoint blob err = %v, want ErrInvalid", err)
	}
}
