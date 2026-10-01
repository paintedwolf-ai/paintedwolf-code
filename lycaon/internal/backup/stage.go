package backup

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/editoroutbox"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fssync"
	"github.com/lycaon/lycaon/internal/localdata"
	"github.com/lycaon/lycaon/internal/sandbox"
)

var stageMu sync.Mutex

// StageOpts configures restore staging.
type StageOpts struct {
	DBPath        string
	ConfigDir     string
	ArchivePath   string
	SQLDB         db.DBTX
	SchemaVersion int
	Now           time.Time
}

// Stage validates an archive and publishes a durable restore transaction.
func Stage(ctx context.Context, opts StageOpts) (StageResult, error) {
	stageMu.Lock()
	defer stageMu.Unlock()
	if opts.ConfigDir == "" {
		return StageResult{}, fmt.Errorf("backup: config dir required")
	}
	archiveInfo, err := os.Stat(opts.ArchivePath)
	if err != nil {
		return StageResult{}, &InvalidError{Detail: "archive could not be opened"}
	}
	if archiveInfo.Size() == 0 {
		return StageResult{}, &InvalidError{Detail: "empty archive"}
	}
	if archiveInfo.Size() > MaxArchiveBytes {
		return StageResult{}, &InvalidError{Detail: "compressed archive exceeds size limit"}
	}
	replacePaths, replaceDirs := localdata.BackupRelPaths(), localdata.BackupRelDirs()
	archive, manifest, entries, err := parseAndValidateArchive(opts.ArchivePath, replacePaths, replaceDirs)
	if err != nil {
		return StageResult{}, err
	}
	defer func() { _ = archive.Close() }()
	return stageManifest(ctx, opts, manifest, false, func(ctx context.Context, fe FileEntry, root, dest string) error {
		return extractZipEntry(ctx, entries[fe.RelPath], root, dest, fe)
	})
}

type stagedFileWriter func(context.Context, FileEntry, string, string) error

func stageManifest(ctx context.Context, opts StageOpts, manifest Manifest, localSnapshot bool, write stagedFileWriter) (StageResult, error) {
	liveName, err := liveStoreFilename(opts.ConfigDir, opts.DBPath)
	if err != nil {
		return StageResult{}, err
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	if opts.SchemaVersion != db.SchemaVersion {
		return StageResult{}, &BaselineMismatchError{
			ArchiveVersion: manifest.SchemaUserVersion,
			BinaryVersion:  opts.SchemaVersion,
		}
	}
	if err := checkManifestShape(ctx, manifest); err != nil {
		return StageResult{}, err
	}
	if err := preflightRestore(ctx, opts, manifest, localSnapshot); err != nil {
		return StageResult{}, err
	}
	markerPath := PendingMarkerPath(opts.ConfigDir)
	previous, err := readReplaceablePending(markerPath)
	if err != nil {
		return StageResult{}, err
	}

	deleteRelPaths := absentReplaceRelPaths(manifest)
	transactionID := uuid.NewString()
	stagingDir := filepath.Join(opts.ConfigDir, localdata.RestoreStagingDirPrefix+"-"+transactionID)
	recoveryDir := filepath.Join(opts.ConfigDir, localdata.RestorePreImageDirPrefix+"-"+transactionID)

	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return StageResult{}, fmt.Errorf("backup: create staging: %w", err)
	}

	pendingFiles := make([]PendingFile, 0, len(manifest.Files))
	for _, fe := range manifest.Files {
		dest, err := joinUnder(stagingDir, fe.RelPath)
		if err != nil {
			_ = os.RemoveAll(stagingDir)
			return StageResult{}, err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			_ = os.RemoveAll(stagingDir)
			return StageResult{}, fmt.Errorf("backup: staging mkdir: %w", err)
		}
		if err := write(ctx, fe, stagingDir, dest); err != nil {
			_ = os.RemoveAll(stagingDir)
			return StageResult{}, err
		}
		pendingFiles = append(pendingFiles, PendingFile{
			RelPath: fe.RelPath,
			Kind:    fe.Kind,
			Mode:    fe.Mode,
			SHA256:  fe.SHA256,
			Size:    fe.Size,
		})
	}
	if err := upgradeStagedStore(ctx, stagingDir, &manifest, pendingFiles); err != nil {
		_ = os.RemoveAll(stagingDir)
		return StageResult{}, err
	}

	if err := validateStagedReferences(ctx, stagingDir, manifest); err != nil {
		_ = os.RemoveAll(stagingDir)
		return StageResult{}, err
	}

	// Stage recovery only after archive verification.
	if err := writeRecoveryCopy(ctx, opts, recoveryDir, manifest.ReplaceRelPaths, manifest.ReplaceRelDirs); err != nil {
		_ = os.RemoveAll(stagingDir)
		_ = os.RemoveAll(recoveryDir)
		return StageResult{}, err
	}

	marker := PendingMarker{
		LiveStoreFilename:   liveName,
		Operation:           PendingOperationRestore,
		StagingDir:          stagingDir,
		RecoveryDir:         recoveryDir,
		SourceAppVersion:    manifest.AppVersion,
		CreatedAt:           now.Format(time.RFC3339),
		Files:               pendingFiles,
		DeleteRelPaths:      deleteRelPaths,
		ReplaceRelDirs:      manifest.ReplaceRelDirs,
		RetainedBranchTrees: manifest.RetainedBranchTrees,
	}
	if err := ctx.Err(); err != nil {
		_ = os.RemoveAll(stagingDir)
		_ = os.RemoveAll(recoveryDir)
		return StageResult{}, err
	}
	if err := publishPendingMarker(markerPath, marker, previous); err != nil {
		var published *markerPublishedError
		if !errors.As(err, &published) {
			_ = os.RemoveAll(stagingDir)
			_ = os.RemoveAll(recoveryDir)
		}
		if os.IsExist(err) {
			return StageResult{}, ErrPending
		}
		return StageResult{}, fmt.Errorf("backup: write pending marker: %w", err)
	}

	return StageResult{
		RestartRequired:       true,
		RecoveryCopyPath:      recoveryDir,
		ReclaimedPreImages:    reclaimSupersededPreImages(opts.ConfigDir, recoveryDir, previous.recoveryDir(opts.ConfigDir)),
		SupersededTransaction: previous.recoveryDir(opts.ConfigDir),
	}, nil
}

// An archive must identify a known shape and a complete route to this binary.
func checkManifestShape(ctx context.Context, manifest Manifest) error {
	_, err := db.PlanSchemaUpgrade(ctx, migrations.Baseline{Revision: manifest.SchemaUserVersion, Shape: manifest.SchemaShapeDigest})
	if err != nil {
		return &BaselineMismatchError{
			ArchiveVersion: manifest.SchemaUserVersion,
			BinaryVersion:  db.SchemaVersion,
			ShapeDetail:    err.Error(),
		}
	}
	return nil
}

// Only the supplied transactions retain their recovery pre-images.
func reclaimSupersededPreImages(configDir string, keep ...string) []localdata.RestorePreImage {
	reclaimed, err := localdata.PruneRestorePreImages(configDir, keep...)
	if err != nil {
		slog.Warn("could not reclaim superseded restore pre-images", "error", err)
	}
	for _, image := range reclaimed {
		slog.Info("reclaimed superseded restore pre-image", "path", image.Path, "bytes", image.Bytes)
	}
	return reclaimed
}

// writeRecoveryCopy snapshots every live path the restore may replace.
func writeRecoveryCopy(ctx context.Context, opts StageOpts, recoveryDir string, replaceRelPaths, replaceRelDirs []string) error {
	liveName, err := liveStoreFilename(opts.ConfigDir, opts.DBPath)
	if err != nil {
		return err
	}
	release := retainCaptureBodies(opts.ConfigDir)
	defer release()
	if err := os.MkdirAll(recoveryDir, 0o700); err != nil {
		return fmt.Errorf("backup: create recovery dir: %w", err)
	}

	for _, dir := range replaceRelDirs {
		if dir == editoroutbox.Directory() {
			if err := captureEditorOutbox(ctx, opts.ConfigDir, recoveryDir); err != nil {
				return err
			}
		}
	}
	paths := make(map[string]struct{}, len(replaceRelPaths))
	for _, rel := range replaceRelPaths {
		paths[rel] = struct{}{}
	}
	if _, replacesStore := paths[storeRelPath]; replacesStore && opts.SQLDB != nil {
		if err := db.CreateSnapshot(ctx, opts.SQLDB, filepath.Join(recoveryDir, storeRelPath)); err != nil {
			return fmt.Errorf("backup: recovery snapshot of store: %w", err)
		}
		if err := syncDirectoryTree(recoveryDir, recoveryDir); err != nil {
			return err
		}
		delete(paths, storeRelPath)
	}
	if _, replacesStore := paths[storeRelPath]; replacesStore && opts.SQLDB == nil {
		// Raw database copies need their journals to preserve uncheckpointed commits.
		for _, suffix := range db.StoreSidecarSuffixes() {
			paths[storeRelPath+suffix] = struct{}{}
		}
	}
	for _, dir := range replaceRelDirs {
		if dir == editoroutbox.Directory() {
			continue
		}
		root := filepath.Join(opts.ConfigDir, filepath.FromSlash(dir))
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(opts.ConfigDir, path)
			if err != nil {
				return err
			}
			if !(branchCapture{all: true}).includes(filepath.ToSlash(rel), entry.IsDir()) {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if path == root || entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
				return fmt.Errorf("durable path is not a regular file: %s", path)
			}
			paths[filepath.ToSlash(rel)] = struct{}{}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("backup: read live %s for recovery: %w", dir, err)
		}
	}

	for rel := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		dest, err := joinUnder(recoveryDir, rel)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return fmt.Errorf("backup: recovery mkdir %s: %w", rel, err)
		}
		src := filepath.Join(opts.ConfigDir, filepath.FromSlash(recoverySourceRelPath(rel, liveName)))
		if err := copyDurableRestoreFile(ctx, recoveryDir, dest, src); err != nil { // #nosec G703 -- dest is beneath the recovery root
			// Added archive paths have no live pre-image.
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("backup: write recovery %s: %w", rel, err)
		}
	}
	return nil
}

// joinUnder rejects paths outside base.
func joinUnder(base, rel string) (string, error) {
	cleanBase := filepath.Clean(base)
	dest := filepath.Join(cleanBase, filepath.FromSlash(rel))
	relToBase, err := filepath.Rel(cleanBase, dest)
	if err != nil || relToBase == ".." || strings.HasPrefix(relToBase, ".."+string(filepath.Separator)) {
		return "", &InvalidError{Detail: "rel_path escapes destination root: " + rel}
	}
	return dest, nil
}

// PendingMarkerPath returns the restore-pending marker path under configDir.
func PendingMarkerPath(configDir string) string {
	return filepath.Join(configDir, PendingMarkerName)
}

func parseAndValidateArchive(path string, expectedPaths, expectedDirs []string) (*zip.ReadCloser, Manifest, map[string]*zip.File, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, Manifest{}, nil, &InvalidError{Detail: "not a zip archive"}
	}
	fail := func(err error) (*zip.ReadCloser, Manifest, map[string]*zip.File, error) {
		_ = zr.Close()
		return nil, Manifest{}, nil, err
	}
	if len(zr.File) > maxArchiveEntries {
		return fail(&InvalidError{Detail: "archive contains too many entries"})
	}
	entries := map[string]*zip.File{}
	var manifestFile *zip.File
	var expanded uint64
	for _, f := range zr.File {
		if f.UncompressedSize64 > MaxExpandedArchiveBytes-expanded {
			return fail(&InvalidError{Detail: "archive exceeds expanded size limit"})
		}
		expanded += f.UncompressedSize64
		name := filepath.ToSlash(f.Name)
		if name == "" || strings.HasPrefix(name, "/") || sandbox.HasParentTraversal(name) {
			return fail(&InvalidError{Detail: "unsafe zip entry path"})
		}
		if _, duplicate := entries[name]; duplicate || name == manifestEntryName && manifestFile != nil {
			return fail(&InvalidError{Detail: "duplicate zip entry " + name})
		}
		if name == manifestEntryName {
			manifestFile = f
		} else {
			entries[name] = f
		}
	}
	if manifestFile == nil {
		return fail(&InvalidError{Detail: "missing manifest.json"})
	}
	manifestRaw, err := readZipEntryBounded(manifestFile)
	if err != nil {
		return fail(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		return fail(&InvalidError{Detail: "manifest.json not valid JSON"})
	}
	sizes := make(map[string]uint64, len(entries))
	for name, entry := range entries {
		sizes[name] = entry.UncompressedSize64
	}
	if err := validateManifestEntries(manifest, expectedPaths, expectedDirs, sizes); err != nil {
		return fail(err)
	}

	return zr, manifest, entries, nil
}

func validateManifestEntries(manifest Manifest, expectedPaths, expectedDirs []string, entries map[string]uint64) error {
	if manifest.FormatVersion != FormatVersion {
		return &InvalidError{Detail: fmt.Sprintf("unsupported format_version %d", manifest.FormatVersion)}
	}
	if len(manifest.Files) == 0 || len(manifest.ReplaceRelPaths) == 0 {
		return &InvalidError{Detail: "manifest lists no files or replacement scope"}
	}
	replaceSet := map[string]struct{}{}
	for _, rel := range manifest.ReplaceRelPaths {
		if !RestorableRelPath(rel) || localdata.IsCredentialRel(rel) {
			return &InvalidError{Detail: "replacement scope contains a non-restorable durable file: " + rel}
		}
		if _, duplicate := replaceSet[rel]; duplicate {
			return &InvalidError{Detail: "duplicate replacement path " + rel}
		}
		replaceSet[rel] = struct{}{}
	}
	if _, ok := replaceSet[storeRelPath]; !ok {
		return &InvalidError{Detail: "replacement scope does not contain store.db"}
	}
	if !matchesReplacementScope(replaceSet, expectedPaths) {
		return &InvalidError{Detail: "replacement scope does not match the durable file set"}
	}
	dirSet := map[string]struct{}{}
	for _, dir := range manifest.ReplaceRelDirs {
		if !RestorableRelDir(dir) {
			return &InvalidError{Detail: "replacement scope contains a non-restorable durable directory: " + dir}
		}
		if _, duplicate := dirSet[dir]; duplicate {
			return &InvalidError{Detail: "duplicate replacement directory " + dir}
		}
		dirSet[dir] = struct{}{}
	}
	if !matchesReplacementScope(dirSet, expectedDirs) {
		return &InvalidError{Detail: "replacement scope does not match the durable directory set"}
	}
	if err := validateBranchTreeList(manifest.RetainedBranchTrees); err != nil {
		return err
	}
	if len(manifest.RetainedBranchTrees) > 0 {
		if _, ok := dirSet[enginepaths.WorkerBranchesDirName]; !ok {
			return &InvalidError{Detail: "worker trees are outside replacement scope"}
		}
	}
	seen := map[string]struct{}{}
	for _, fe := range manifest.Files {
		validKind := fe.Kind == fileKindRegular || fe.Kind == fileKindSymlink
		validMode := fe.Mode&^uint32(0o777) == 0
		if fe.RelPath == "" || fe.Size < 0 || fe.Size == math.MaxInt64 || !validSHA256(fe.SHA256) || !validKind || !validMode ||
			sandbox.HasParentTraversal(fe.RelPath) || !restorableArchivePath(fe.RelPath, manifest.RetainedBranchTrees) {
			return &InvalidError{Detail: "invalid durable file in manifest: " + fe.RelPath}
		}
		if _, duplicate := seen[fe.RelPath]; duplicate {
			return &InvalidError{Detail: "duplicate rel_path " + fe.RelPath}
		}
		seen[fe.RelPath] = struct{}{}
		if !replaceScopeContains(replaceSet, manifest.ReplaceRelDirs, fe.RelPath) {
			return &InvalidError{Detail: "manifest file is outside replacement scope: " + fe.RelPath}
		}
		entry, ok := entries[fe.RelPath]
		if !ok {
			return &InvalidError{Detail: "manifest file missing from zip: " + fe.RelPath}
		}
		if entry != uint64(fe.Size) {
			return &InvalidError{Detail: "size mismatch for " + fe.RelPath}
		}
		if fe.Kind == fileKindSymlink && (fe.Size == 0 || fe.Size > maxSymlinkTargetBytes) {
			return &InvalidError{Detail: "symlink target exceeds size limit for " + fe.RelPath}
		}
		if fe.RelPath == storeRelPath && fe.Kind != fileKindRegular {
			return &InvalidError{Detail: "store.db must be a regular file"}
		}
		if strings.HasPrefix(fe.RelPath, enginepaths.WorkerBranchesDirName+"/") && workerMetadataPath(fe.RelPath, false) && fe.Kind != fileKindRegular {
			return &InvalidError{Detail: "worker layout metadata must be a regular file"}
		}
	}
	if _, ok := seen[storeRelPath]; !ok {
		return &InvalidError{Detail: "manifest does not contain store.db"}
	}
	for rel := range entries {
		if _, listed := seen[rel]; !listed {
			return &InvalidError{Detail: "zip entry is not listed in manifest: " + rel}
		}
	}
	return nil
}

func matchesReplacementScope(actual map[string]struct{}, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	for _, path := range expected {
		if _, ok := actual[path]; !ok {
			return false
		}
	}
	return true
}

func replaceScopeContains(files map[string]struct{}, dirs []string, rel string) bool {
	if _, ok := files[rel]; ok {
		return true
	}
	for _, dir := range dirs {
		if strings.HasPrefix(rel, dir+"/") {
			return true
		}
	}
	return false
}

func absentReplaceRelPaths(manifest Manifest) []string {
	present := make(map[string]struct{}, len(manifest.Files))
	for _, fe := range manifest.Files {
		present[fe.RelPath] = struct{}{}
	}
	var missing []string
	for _, rel := range manifest.ReplaceRelPaths {
		if _, ok := present[rel]; !ok {
			missing = append(missing, rel)
		}
	}
	return missing
}

func readZipEntryBounded(entry *zip.File) ([]byte, error) {
	if entry.UncompressedSize64 > uint64(maxManifestBytes) { //nolint:gosec // G115 — archiveLimits panics at init unless the manifest bound is positive
		return nil, &InvalidError{Detail: "manifest.json exceeds size limit"}
	}
	rc, err := entry.Open()
	if err != nil {
		return nil, &InvalidError{Detail: "unreadable zip entry " + entry.Name}
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(io.LimitReader(rc, int64(maxManifestBytes)+1))
	if err != nil {
		return nil, &InvalidError{Detail: "read zip entry " + entry.Name}
	}
	if len(data) > maxManifestBytes {
		return nil, &InvalidError{Detail: "manifest.json exceeds size limit"}
	}
	return data, nil
}

func extractZipEntry(ctx context.Context, entry *zip.File, stagingRoot, dest string, fe FileEntry) error {
	rc, err := entry.Open()
	if err != nil {
		return &InvalidError{Detail: "unreadable zip entry " + entry.Name}
	}
	defer func() { _ = rc.Close() }()
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fileMode) // #nosec G703 -- dest from joinUnder after the durable-path gate
	if err != nil {
		return fmt.Errorf("backup: staging create %s: %w", fe.RelPath, err)
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), contextReader{ctx: ctx, in: io.LimitReader(rc, fe.Size+1)})
	if copyErr == nil {
		copyErr = fssync.File(f)
	}
	closeErr := f.Close()
	if copyErr != nil {
		return fmt.Errorf("backup: staging write %s: %w", fe.RelPath, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("backup: staging close %s: %w", fe.RelPath, closeErr)
	}
	if n != fe.Size || hex.EncodeToString(h.Sum(nil)) != fe.SHA256 {
		return &InvalidError{Detail: "staged entry changed during extraction: " + fe.RelPath}
	}
	return syncDirectoryTree(stagingRoot, filepath.Dir(dest))
}

func validateStagedStore(ctx context.Context, path string, schemaVersion int) error {
	sqlDB, err := db.OpenReadOnly(ctx, path)
	if err != nil {
		return &InvalidError{Detail: "store.db is not a readable SQLite database"}
	}
	defer func() { _ = sqlDB.Close() }()
	version, err := db.ReadUserVersion(ctx, sqlDB)
	if err != nil {
		return &InvalidError{Detail: "store.db schema version is unreadable"}
	}
	if version != schemaVersion {
		return &BaselineMismatchError{ArchiveVersion: version, BinaryVersion: schemaVersion}
	}
	// Require the startup schema before replacing live state.
	shapeDiff, err := db.BaselineShapeDiff(ctx, sqlDB)
	if err != nil {
		return &InvalidError{Detail: "store.db structure could not be read"}
	}
	if len(shapeDiff) > 0 {
		return &BaselineMismatchError{
			ArchiveVersion: version,
			BinaryVersion:  schemaVersion,
			ShapeDetail:    strings.Join(shapeDiff[:min(len(shapeDiff), 3)], "; "),
		}
	}
	rows, err := sqlDB.QueryContext(ctx, `PRAGMA quick_check`)
	if err != nil {
		return &InvalidError{Detail: "store.db integrity check could not run"}
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil || result != "ok" {
			return &InvalidError{Detail: "store.db failed its integrity check"}
		}
	}
	if err := rows.Err(); err != nil {
		return &InvalidError{Detail: "store.db integrity check could not finish"}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	foreignKeys, err := sqlDB.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return &InvalidError{Detail: "store.db reference check could not run"}
	}
	defer func() { _ = foreignKeys.Close() }()
	if foreignKeys.Next() {
		return &InvalidError{Detail: "store.db contains broken relational references"}
	}
	if err := foreignKeys.Err(); err != nil {
		return &InvalidError{Detail: "store.db reference check could not finish"}
	}
	return nil
}

func copyDurableRestoreFile(ctx context.Context, root, dest, src string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := os.Symlink(target, dest); err != nil {
			return err
		}
		return syncDirectoryTree(root, filepath.Dir(dest))
	}
	if err := copySnapshotRegular(ctx, src, dest, info.Mode().Perm()); err != nil {
		return err
	}

	return syncDirectoryTree(root, filepath.Dir(dest))
}
