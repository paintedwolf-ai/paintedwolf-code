package backup_test

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

type seededCheckpointFixture struct {
	database  *db.Store
	root      string
	branchRel string
	storeDir  string
	baseSHA   string
	finalSHA  string
	finalPath string
	commitOID string
	treeOID   string
	blobOID   string
}

func setupCheckpointBackupFixture(t *testing.T) seededCheckpointFixture {
	t.Helper()
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

	srcRepo := t.TempDir()
	testutil.FailErr(t, "write repo file", os.WriteFile(filepath.Join(srcRepo, "main.go"), []byte("package after\n"), 0o644))
	gittest.InitCommit(t, srcRepo, "worker commit")
	commitOID := strings.TrimSpace(gittest.Run(t, srcRepo, "rev-parse", "HEAD"))
	treeOID := strings.TrimSpace(gittest.Run(t, srcRepo, "rev-parse", "HEAD^{tree}"))
	blobOID := strings.TrimSpace(gittest.Run(t, srcRepo, "rev-parse", "HEAD:main.go"))

	retainedObjectsDir := filepath.Join(root, "worker-branches", ".adapter", "privategit", "retained-store", "objects")
	copyGitObjectsDir(t, filepath.Join(srcRepo, ".git", "objects"), retainedObjectsDir)

	fx := seededCheckpointFixture{
		database:  database,
		root:      root,
		branchRel: branchRel,
		storeDir:  storeDir,
		baseSHA:   baseSHA,
		finalSHA:  finalSHA,
		finalPath: finalPath,
		commitOID: commitOID,
		treeOID:   treeOID,
		blobOID:   blobOID,
	}
	writeCheckpointManifestAndMeta(t, fx, checkpoint.PrivateGitSnapshot{
		HeadRef:         "refs/heads/main",
		Refs:            map[string]string{"refs/heads/main": commitOID},
		RetainedObjects: []string{commitOID, treeOID, blobOID},
	})
	return fx
}

func writeCheckpointManifestAndMeta(t *testing.T, fx seededCheckpointFixture, pg checkpoint.PrivateGitSnapshot) {
	t.Helper()
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
			Final:          layeredfs.ChangeFinalAttr{Mode: 0o644, Size: int64(len("package after\n")), SHA256: fx.finalSHA},
			Base:           layeredfs.BaseCapture{Kind: layeredfs.EntryRegular, Mode: 0o644, Size: int64(len("package before\n")), SHA256: fx.baseSHA},
		}},
		PrivateGit: pg,
	}
	cpBytes, err := json.MarshalIndent(cpManifest, "", "  ")
	testutil.FailErr(t, "marshal checkpoint manifest", err)
	cpManifestPath := filepath.Join(fx.storeDir, "checkpoints", "cp-1", "manifest.json")
	testutil.FailErr(t, "mkdir checkpoint manifest", os.MkdirAll(filepath.Dir(cpManifestPath), 0o700))
	_ = os.Chmod(cpManifestPath, 0o600)
	testutil.FailErr(t, "write checkpoint manifest", os.WriteFile(cpManifestPath, cpBytes, 0o400))

	metaPath := filepath.Join(fx.root, filepath.FromSlash(fx.branchRel+".meta/state.json"))
	testutil.FailErr(t, "mkdir branch meta", os.MkdirAll(filepath.Dir(metaPath), 0o700))
	metaJSON := []byte(`{"format_version":2,"roots":[{"id":"primary","path":"/tmp/project","label":"primary","is_primary":true}],"snapshot_complete":true,"last_published_checkpoint_id":"cp-1","checkpoint_store_dir":"` + filepath.ToSlash(fx.storeDir) + `"}`)
	testutil.FailErr(t, "write branch meta", os.WriteFile(metaPath, metaJSON, 0o600))
}

func copyGitObjectsDir(t *testing.T, srcObjects, dstObjects string) {
	t.Helper()
	testutil.FailErr(t, "walk git objects", filepath.WalkDir(srcObjects, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		rel, err := filepath.Rel(srcObjects, path)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		dst := filepath.Join(dstObjects, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		return os.WriteFile(dst, raw, 0o444)
	}))
}

func TestBackupValidatesPublishedWorkerCheckpointsAndGitObjects(t *testing.T) {
	fx := setupCheckpointBackupFixture(t)

	// Even with the branch working tree evicted on disk, Create succeeds because the checkpoint and retained Git objects are complete.
	opts := backup.CreateOpts{ConfigDir: fx.root, DBPath: filepath.Join(fx.root, "store.db"), SQLDB: fx.database, AppVersion: "test", SchemaUserVersion: db.SchemaVersion}
	rawArchive, manifest, err := createArchive(t, t.Context(), opts)
	testutil.FailErr(t, "create archive for evicted checkpoint-backed worker", err)
	if slices.Contains(manifest.RetainedBranchTrees, fx.branchRel) {
		t.Fatalf("sealed checkpoint-backed worker should not require a retained branch tree: %+v", manifest.RetainedBranchTrees)
	}

	// Omitting the checkpoint blob from the archive must be rejected by Stage.
	blobRel := "worker-recovery/checkpoints/blobs/" + fx.finalSHA
	missingBlobZip := omitRetainedFile(t, rawArchive, manifest, blobRel)
	missingBlobPath := filepath.Join(t.TempDir(), "missing-blob.zip")
	testutil.FailErr(t, "write missing-blob archive", os.WriteFile(missingBlobPath, missingBlobZip, 0o600))
	if _, err := backup.Stage(t.Context(), backup.StageOpts{ConfigDir: t.TempDir(), ArchivePath: missingBlobPath, SchemaVersion: db.SchemaVersion}); !errors.Is(err, backup.ErrInvalid) {
		t.Fatalf("Stage with missing checkpoint blob err = %v, want ErrInvalid", err)
	}

	// Omitting a reachable blob object from the archive must be rejected by Stage.
	gitBlobRel := "worker-branches/.adapter/privategit/retained-store/objects/" + fx.blobOID[:2] + "/" + fx.blobOID[2:]
	missingGitZip := omitRetainedFile(t, rawArchive, manifest, gitBlobRel)
	missingGitPath := filepath.Join(t.TempDir(), "missing-git.zip")
	testutil.FailErr(t, "write missing-git archive", os.WriteFile(missingGitPath, missingGitZip, 0o600))
	if _, err := backup.Stage(t.Context(), backup.StageOpts{ConfigDir: t.TempDir(), ArchivePath: missingGitPath, SchemaVersion: db.SchemaVersion}); !errors.Is(err, backup.ErrInvalid) {
		t.Fatalf("Stage with missing retained git blob err = %v, want ErrInvalid", err)
	}

	// Removing the final blob on disk must cause backup.Create to fail with ErrInvalid.
	testutil.FailErr(t, "chmod final blob", os.Chmod(fx.finalPath, 0o600))
	testutil.FailErr(t, "remove final blob", os.Remove(fx.finalPath))
	if _, err := backup.Create(t.Context(), opts, filepath.Join(t.TempDir(), "broken.zip")); !errors.Is(err, backup.ErrInvalid) {
		t.Fatalf("Create with missing checkpoint blob err = %v, want ErrInvalid", err)
	}
}

func TestBackupRejectsIncompleteOrCorruptCheckpointGitClosures(t *testing.T) {
	t.Run("unrelated_valid_pack_and_index", func(t *testing.T) {
		fx := setupCheckpointBackupFixture(t)
		retainedObjectsDir := filepath.Join(fx.root, "worker-branches", ".adapter", "privategit", "retained-store", "objects")
		testutil.FailErr(t, "clear retained objects", os.RemoveAll(retainedObjectsDir))

		otherRepo := t.TempDir()
		testutil.FailErr(t, "write unrelated file", os.WriteFile(filepath.Join(otherRepo, "other.txt"), []byte("unrelated content\n"), 0o644))
		gittest.InitCommit(t, otherRepo, "unrelated commit")
		gittest.Run(t, otherRepo, "repack", "-a", "-d")
		gittest.Run(t, otherRepo, "prune-packed")
		copyGitObjectsDir(t, filepath.Join(otherRepo, ".git", "objects"), retainedObjectsDir)

		opts := backup.CreateOpts{ConfigDir: fx.root, DBPath: filepath.Join(fx.root, "store.db"), SQLDB: fx.database, AppVersion: "test", SchemaUserVersion: db.SchemaVersion}
		if _, err := backup.Create(t.Context(), opts, filepath.Join(t.TempDir(), "unrelated-pack.zip")); !errors.Is(err, backup.ErrInvalid) {
			t.Fatalf("Create with unrelated valid pack+idx err = %v, want ErrInvalid", err)
		}
	})

	t.Run("mismatched_pack_and_index_basenames", func(t *testing.T) {
		fx := setupCheckpointBackupFixture(t)
		packDir := filepath.Join(fx.root, "worker-branches", ".adapter", "privategit", "retained-store", "objects", "pack")
		testutil.FailErr(t, "mkdir packDir", os.MkdirAll(packDir, 0o700))
		testutil.FailErr(t, "write orphan pack", os.WriteFile(filepath.Join(packDir, "pack-"+strings.Repeat("1", 40)+".pack"), []byte("PACK"), 0o444))
		testutil.FailErr(t, "write orphan idx", os.WriteFile(filepath.Join(packDir, "pack-"+strings.Repeat("2", 40)+".idx"), []byte("IDX"), 0o444))

		opts := backup.CreateOpts{ConfigDir: fx.root, DBPath: filepath.Join(fx.root, "store.db"), SQLDB: fx.database, AppVersion: "test", SchemaUserVersion: db.SchemaVersion}
		if _, err := backup.Create(t.Context(), opts, filepath.Join(t.TempDir(), "mismatched-names.zip")); !errors.Is(err, backup.ErrInvalid) {
			t.Fatalf("Create with mismatched pack/idx basenames err = %v, want ErrInvalid", err)
		}
	})

	t.Run("mismatched_pack_and_index_contents", func(t *testing.T) {
		fx := setupCheckpointBackupFixture(t)
		retainedObjectsDir := filepath.Join(fx.root, "worker-branches", ".adapter", "privategit", "retained-store", "objects")
		testutil.FailErr(t, "clear retained objects", os.RemoveAll(retainedObjectsDir))

		repoA := t.TempDir()
		testutil.FailErr(t, "write repoA", os.WriteFile(filepath.Join(repoA, "a.txt"), []byte("repo A\n"), 0o644))
		gittest.InitCommit(t, repoA, "commit A")
		gittest.Run(t, repoA, "repack", "-a", "-d")

		repoB := t.TempDir()
		testutil.FailErr(t, "write repoB", os.WriteFile(filepath.Join(repoB, "b.txt"), []byte("repo B\n"), 0o644))
		gittest.InitCommit(t, repoB, "commit B")
		gittest.Run(t, repoB, "repack", "-a", "-d")

		packA := findSinglePackBase(t, filepath.Join(repoA, ".git", "objects", "pack"))
		packB := findSinglePackBase(t, filepath.Join(repoB, ".git", "objects", "pack"))
		dstPackDir := filepath.Join(retainedObjectsDir, "pack")
		testutil.FailErr(t, "mkdir dstPackDir", os.MkdirAll(dstPackDir, 0o700))
		rawPackA, err := os.ReadFile(filepath.Join(repoA, ".git", "objects", "pack", packA+".pack"))
		testutil.FailErr(t, "read packA", err)
		rawIdxB, err := os.ReadFile(filepath.Join(repoB, ".git", "objects", "pack", packB+".idx"))
		testutil.FailErr(t, "read idxB", err)
		testutil.FailErr(t, "write packA", os.WriteFile(filepath.Join(dstPackDir, packA+".pack"), rawPackA, 0o444))
		testutil.FailErr(t, "write mismatched idxB as packA.idx", os.WriteFile(filepath.Join(dstPackDir, packA+".idx"), rawIdxB, 0o444))

		opts := backup.CreateOpts{ConfigDir: fx.root, DBPath: filepath.Join(fx.root, "store.db"), SQLDB: fx.database, AppVersion: "test", SchemaUserVersion: db.SchemaVersion}
		if _, err := backup.Create(t.Context(), opts, filepath.Join(t.TempDir(), "mismatched-content.zip")); !errors.Is(err, backup.ErrInvalid) {
			t.Fatalf("Create with mismatched pack/idx content err = %v, want ErrInvalid", err)
		}
	})

	t.Run("corrupt_loose_object", func(t *testing.T) {
		fx := setupCheckpointBackupFixture(t)
		looseBlob := filepath.Join(fx.root, "worker-branches", ".adapter", "privategit", "retained-store", "objects", fx.blobOID[:2], fx.blobOID[2:])
		testutil.FailErr(t, "chmod loose blob", os.Chmod(looseBlob, 0o600))
		testutil.FailErr(t, "corrupt loose blob", os.WriteFile(looseBlob, []byte("corrupt-git-loose-object-bytes"), 0o444))

		opts := backup.CreateOpts{ConfigDir: fx.root, DBPath: filepath.Join(fx.root, "store.db"), SQLDB: fx.database, AppVersion: "test", SchemaUserVersion: db.SchemaVersion}
		if _, err := backup.Create(t.Context(), opts, filepath.Join(t.TempDir(), "corrupt-loose.zip")); !errors.Is(err, backup.ErrInvalid) {
			t.Fatalf("Create with corrupt loose object err = %v, want ErrInvalid", err)
		}
	})

	t.Run("missing_reachable_blob", func(t *testing.T) {
		fx := setupCheckpointBackupFixture(t)
		// Only list commitOID in RetainedObjects while deleting the reachable blob object from disk.
		writeCheckpointManifestAndMeta(t, fx, checkpoint.PrivateGitSnapshot{
			HeadRef:         "refs/heads/main",
			Refs:            map[string]string{"refs/heads/main": fx.commitOID},
			RetainedObjects: []string{fx.commitOID},
		})
		looseBlob := filepath.Join(fx.root, "worker-branches", ".adapter", "privategit", "retained-store", "objects", fx.blobOID[:2], fx.blobOID[2:])
		testutil.FailErr(t, "chmod loose blob", os.Chmod(looseBlob, 0o600))
		testutil.FailErr(t, "remove loose blob", os.Remove(looseBlob))

		opts := backup.CreateOpts{ConfigDir: fx.root, DBPath: filepath.Join(fx.root, "store.db"), SQLDB: fx.database, AppVersion: "test", SchemaUserVersion: db.SchemaVersion}
		if _, err := backup.Create(t.Context(), opts, filepath.Join(t.TempDir(), "missing-blob.zip")); !errors.Is(err, backup.ErrInvalid) {
			t.Fatalf("Create with missing reachable blob err = %v, want ErrInvalid", err)
		}
	})

	t.Run("missing_packed_delta_base", func(t *testing.T) {
		fx := setupCheckpointBackupFixture(t)
		retainedObjectsDir := filepath.Join(fx.root, "worker-branches", ".adapter", "privategit", "retained-store", "objects")
		testutil.FailErr(t, "clear retained objects", os.RemoveAll(retainedObjectsDir))

		deltaRepo := t.TempDir()
		baseText := strings.Repeat("painted wolf delta compression base line\n", 256)
		testutil.FailErr(t, "write base file", os.WriteFile(filepath.Join(deltaRepo, "large.txt"), []byte(baseText), 0o644))
		gittest.InitCommit(t, deltaRepo, "base commit")
		baseCommit := strings.TrimSpace(gittest.Run(t, deltaRepo, "rev-parse", "HEAD"))
		baseBlob := strings.TrimSpace(gittest.Run(t, deltaRepo, "rev-parse", "HEAD:large.txt"))

		testutil.FailErr(t, "write delta file", os.WriteFile(filepath.Join(deltaRepo, "large.txt"), []byte(baseText+"modified tail\n"), 0o644))
		gittest.Run(t, deltaRepo, "add", "large.txt")
		gittest.Run(t, deltaRepo, "commit", "-m", "delta commit")
		deltaCommit := strings.TrimSpace(gittest.Run(t, deltaRepo, "rev-parse", "HEAD"))
		deltaTree := strings.TrimSpace(gittest.Run(t, deltaRepo, "rev-parse", "HEAD^{tree}"))
		deltaBlob := strings.TrimSpace(gittest.Run(t, deltaRepo, "rev-parse", "HEAD:large.txt"))

		// Build a thin pack in deltaRepo containing deltaCommit/deltaTree/deltaBlob delta-compressed against baseBlob,
		// without including baseBlob/baseCommit in the pack.
		dstPackDir := filepath.Join(retainedObjectsDir, "pack")
		testutil.FailErr(t, "mkdir dstPackDir", os.MkdirAll(dstPackDir, 0o700))
		packInput := strings.Join([]string{deltaCommit, deltaTree, deltaBlob, "^" + baseCommit, "^" + baseBlob}, "\n") + "\n"
		thinPackBytes, code, err := gitexec.Run(t.Context(), deltaRepo, []string{"pack-objects", "--stdout", "--thin", "--delta-base-offset"}, gitexec.Opts{
			Profile: gitexec.ProfileHermetic,
			Input:   []byte(packInput),
		})
		if err != nil || code != 0 {
			t.Fatalf("git pack-objects --stdout --thin failed (exit %d): %v (%s)", code, err, thinPackBytes)
		}
		tmpPack := filepath.Join(deltaRepo, "pack-thin.pack")
		tmpIdx := filepath.Join(deltaRepo, "pack-thin.idx")
		testutil.FailErr(t, "write tmpPack", os.WriteFile(tmpPack, thinPackBytes, 0o600))
		if out, code, err := gitexec.Run(t.Context(), deltaRepo, []string{"index-pack", "-o", tmpIdx, tmpPack}, gitexec.Opts{
			Profile: gitexec.ProfileHermetic,
		}); err != nil || code != 0 {
			t.Fatalf("git index-pack failed (exit %d): %v (%s)", code, err, out)
		}
		const packBase = "pack-0123456789abcdef0123456789abcdef01234567"
		testutil.FailErr(t, "install thin .pack", os.WriteFile(filepath.Join(dstPackDir, packBase+".pack"), thinPackBytes, 0o444))
		idxBytes, err := os.ReadFile(tmpIdx)
		testutil.FailErr(t, "read tmpIdx", err)
		testutil.FailErr(t, "install thin .idx", os.WriteFile(filepath.Join(dstPackDir, packBase+".idx"), idxBytes, 0o444))

		writeCheckpointManifestAndMeta(t, fx, checkpoint.PrivateGitSnapshot{
			HeadRef:         "refs/heads/main",
			Refs:            map[string]string{"refs/heads/main": deltaCommit},
			RetainedObjects: []string{deltaCommit},
		})

		opts := backup.CreateOpts{ConfigDir: fx.root, DBPath: filepath.Join(fx.root, "store.db"), SQLDB: fx.database, AppVersion: "test", SchemaUserVersion: db.SchemaVersion}
		if _, err := backup.Create(t.Context(), opts, filepath.Join(t.TempDir(), "thin-pack.zip")); !errors.Is(err, backup.ErrInvalid) {
			t.Fatalf("Create with missing packed delta base err = %v, want ErrInvalid", err)
		}
	})

	t.Run("invalid_oid_in_private_git_manifest", func(t *testing.T) {
		fx := setupCheckpointBackupFixture(t)
		writeCheckpointManifestAndMeta(t, fx, checkpoint.PrivateGitSnapshot{
			HeadRef:         "refs/heads/main",
			Refs:            map[string]string{"refs/heads/main": "not-a-valid-git-oid"},
			RetainedObjects: []string{fx.commitOID},
		})
		opts := backup.CreateOpts{ConfigDir: fx.root, DBPath: filepath.Join(fx.root, "store.db"), SQLDB: fx.database, AppVersion: "test", SchemaUserVersion: db.SchemaVersion}
		if _, err := backup.Create(t.Context(), opts, filepath.Join(t.TempDir(), "invalid-ref-oid.zip")); !errors.Is(err, backup.ErrInvalid) {
			t.Fatalf("Create with invalid ref OID err = %v, want ErrInvalid", err)
		}

		writeCheckpointManifestAndMeta(t, fx, checkpoint.PrivateGitSnapshot{
			HeadRef:         "refs/heads/main",
			Refs:            map[string]string{"refs/heads/main": fx.commitOID},
			RetainedObjects: []string{fx.commitOID, "pack:../evil"},
		})
		if _, err := backup.Create(t.Context(), opts, filepath.Join(t.TempDir(), "invalid-pack-ref.zip")); !errors.Is(err, backup.ErrInvalid) {
			t.Fatalf("Create with invalid retained pack entry err = %v, want ErrInvalid", err)
		}
	})
}

func TestBackupPackedWorkerGitHistoryRestoresWithoutOriginalTrunkOrStore(t *testing.T) {
	fx := setupCheckpointBackupFixture(t)
	retainedObjectsDir := filepath.Join(fx.root, "worker-branches", ".adapter", "privategit", "retained-store", "objects")
	testutil.FailErr(t, "clear loose retained objects", os.RemoveAll(retainedObjectsDir))

	packedRepo := t.TempDir()
	testutil.FailErr(t, "write initial", os.WriteFile(filepath.Join(packedRepo, "main.go"), []byte("package before\n"), 0o644))
	gittest.InitCommit(t, packedRepo, "initial")
	initialOID := strings.TrimSpace(gittest.Run(t, packedRepo, "rev-parse", "HEAD"))

	testutil.FailErr(t, "write worker edit", os.WriteFile(filepath.Join(packedRepo, "main.go"), []byte("package after\n"), 0o644))
	gittest.Run(t, packedRepo, "add", "main.go")
	gittest.Run(t, packedRepo, "commit", "-m", "worker packed commit")
	workerOID := strings.TrimSpace(gittest.Run(t, packedRepo, "rev-parse", "HEAD"))
	gittest.Run(t, packedRepo, "repack", "-a", "-d")
	gittest.Run(t, packedRepo, "prune-packed")

	packBase := findSinglePackBase(t, filepath.Join(packedRepo, ".git", "objects", "pack"))
	copyGitObjectsDir(t, filepath.Join(packedRepo, ".git", "objects"), retainedObjectsDir)

	writeCheckpointManifestAndMeta(t, fx, checkpoint.PrivateGitSnapshot{
		HeadRef:         "refs/heads/main",
		Refs:            map[string]string{"refs/heads/main": workerOID},
		RetainedObjects: []string{initialOID, workerOID, "pack:" + packBase},
	})

	archivePath := filepath.Join(t.TempDir(), "packed-history.zip")
	opts := backup.CreateOpts{ConfigDir: fx.root, DBPath: filepath.Join(fx.root, "store.db"), SQLDB: fx.database, AppVersion: "test", SchemaUserVersion: db.SchemaVersion}
	_, err := backup.Create(t.Context(), opts, archivePath)
	testutil.FailErr(t, "create archive with packed git history", err)

	// Remove both the original installation and the source repository before staging and applying the backup.
	testutil.FailErr(t, "close original db", fx.database.Close())
	testutil.FailErr(t, "remove original configDir", os.RemoveAll(fx.root))
	testutil.FailErr(t, "remove original source repo", os.RemoveAll(packedRepo))

	restoredRoot := t.TempDir()
	_, err = backup.Stage(t.Context(), backup.StageOpts{ConfigDir: restoredRoot, ArchivePath: archivePath, SchemaVersion: db.SchemaVersion})
	testutil.FailErr(t, "stage packed history archive", err)
	testutil.FailErr(t, "apply packed history archive", backup.ApplyPending(t.Context(), restoredRoot))

	// Verify the restored packed object store in isolation using a bare Git repo.
	verifyRepo := t.TempDir()
	gittest.Run(t, verifyRepo, "init", "--bare", "-q")
	copyGitObjectsDir(t, filepath.Join(restoredRoot, "worker-branches", ".adapter", "privategit", "retained-store", "objects"), filepath.Join(verifyRepo, "objects"))
	gittest.Run(t, verifyRepo, "fsck", "--full", "--strict")
	gotTree := strings.TrimSpace(gittest.Run(t, verifyRepo, "rev-parse", workerOID+"^{tree}"))
	if gotTree == "" {
		t.Fatal("expected readable tree from restored packed commit")
	}
	gotBlob := gittest.Run(t, verifyRepo, "cat-file", "-p", workerOID+":main.go")
	if gotBlob != "package after\n" {
		t.Fatalf("restored packed blob content = %q, want %q", gotBlob, "package after\n")
	}
}

func findSinglePackBase(t *testing.T, packDir string) string {
	t.Helper()
	entries, err := os.ReadDir(packDir)
	testutil.FailErr(t, "read pack dir", err)
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".pack") {
			return strings.TrimSuffix(e.Name(), ".pack")
		}
	}
	t.Fatalf("no .pack file found in %s", packDir)
	return ""
}
