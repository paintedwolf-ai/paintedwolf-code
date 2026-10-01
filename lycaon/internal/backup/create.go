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
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/editoroutbox"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/localdata"
)

// CreateOpts configures archive production.
type CreateOpts struct {
	ConfigDir         string
	SQLDB             db.DBTX
	DBPath            string
	AppVersion        string
	SchemaUserVersion int
	Now               time.Time
}

type archiveSource struct {
	path string
	kind string
	mode os.FileMode
}

// Create streams a zip archive of the durable installation to destPath.
func Create(ctx context.Context, opts CreateOpts, destPath string) (Manifest, error) {
	release := retainCaptureBodies(opts.ConfigDir)
	defer release()
	if opts.SQLDB == nil {
		return Manifest{}, fmt.Errorf("backup: sql db required")
	}
	if opts.ConfigDir == "" || opts.DBPath == "" || destPath == "" {
		return Manifest{}, fmt.Errorf("backup: config dir, db path, and destination required")
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	tmpDir, err := os.MkdirTemp(filepath.Dir(destPath), snapshotTempPrefix+"*")
	if err != nil {
		return Manifest{}, fmt.Errorf("backup: temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	if err := captureEditorOutbox(ctx, opts.ConfigDir, tmpDir); err != nil {
		return Manifest{}, err
	}
	snapshotPath := filepath.Join(tmpDir, storeRelPath)
	if err := db.CreateSnapshot(ctx, opts.SQLDB, snapshotPath); err != nil {
		return Manifest{}, fmt.Errorf("backup: snapshot store: %w", err)
	}

	snapshot, err := db.OpenReadOnly(ctx, snapshotPath)
	if err != nil {
		return Manifest{}, fmt.Errorf("backup: open captured store: %w", err)
	}
	defer func() { _ = snapshot.Close() }()
	branches, err := unsealedBranchTrees(ctx, snapshot)
	if err != nil {
		return Manifest{}, err
	}
	if err := branches.verifyPresent(opts.ConfigDir); err != nil {
		return Manifest{}, err
	}

	files := map[string]archiveSource{
		storeRelPath: {path: snapshotPath, kind: fileKindRegular, mode: fileMode},
	}
	replaceRelPaths := localdata.BackupRelPaths()
	for _, rel := range replaceRelPaths {
		if rel == storeRelPath {
			continue
		}
		abs := filepath.Join(opts.ConfigDir, filepath.FromSlash(rel))
		if info, statErr := os.Lstat(abs); statErr == nil {
			if !info.Mode().IsRegular() {
				return Manifest{}, fmt.Errorf("backup: durable path %s is not a regular file", rel)
			}
			files[rel] = archiveSource{path: abs, kind: fileKindRegular, mode: info.Mode().Perm()}
		} else if !os.IsNotExist(statErr) {
			return Manifest{}, fmt.Errorf("backup: inspect %s: %w", rel, statErr)
		}
	}

	replaceRelDirs := localdata.BackupRelDirs()
	for _, dir := range replaceRelDirs {
		base := opts.ConfigDir
		if dir == editoroutbox.Directory() {
			base = tmpDir
		}
		if err := collectDurableDir(ctx, base, dir, files, branches, maxArchiveEntries); err != nil {
			return Manifest{}, err
		}
	}

	if err := validateArchiveReferences(ctx, snapshot, files); err != nil {
		return Manifest{}, fmt.Errorf("%w: %w", ErrCaptureIncomplete, err)
	}

	shapeDigest, err := db.ShapeDigest(ctx, snapshot)
	if err != nil {
		return Manifest{}, fmt.Errorf("backup: read store shape: %w", err)
	}

	manifest := Manifest{
		FormatVersion:       FormatVersion,
		AppVersion:          opts.AppVersion,
		SchemaUserVersion:   opts.SchemaUserVersion,
		SchemaShapeDigest:   shapeDigest,
		CreatedAt:           now.Format(time.RFC3339),
		ReplaceRelPaths:     replaceRelPaths,
		ReplaceRelDirs:      replaceRelDirs,
		RetainedBranchTrees: branches.names(),
		Files:               make([]FileEntry, 0, len(files)),
	}
	for rel, source := range files {
		sum, size, hashErr := sourceDigest(ctx, source)
		if hashErr != nil {
			return Manifest{}, fmt.Errorf("%w: hash %s: %w", ErrCaptureIncomplete, rel, hashErr)
		}
		manifest.Files = append(manifest.Files, FileEntry{
			RelPath: rel, Kind: source.kind, Mode: uint32(source.mode.Perm()), Size: size, SHA256: sum,
		})
	}
	sort.Slice(manifest.Files, func(i, j int) bool {
		return manifest.Files[i].RelPath < manifest.Files[j].RelPath
	})

	digest, err := writeArchive(ctx, destPath, manifest, files)
	if err != nil {
		return Manifest{}, err
	}
	manifest.ArchiveSHA256 = digest
	return manifest, nil
}

func collectDurableDir(ctx context.Context, configDir, relDir string, files map[string]archiveSource, branches branchCapture, entryLimit int) error {
	root := filepath.Join(configDir, filepath.FromSlash(relDir))
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == root && os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == root && !entry.IsDir() {
			return fmt.Errorf("durable directory %s is not a directory", relDir)
		}
		rel, err := filepath.Rel(configDir, path)
		if err != nil {
			return err
		}
		if !branches.includes(filepath.ToSlash(rel), entry.IsDir()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		kind := fileKindRegular
		if info.Mode()&os.ModeSymlink != 0 {
			kind = fileKindSymlink
		} else if !info.Mode().IsRegular() {
			return fmt.Errorf("durable path is not a regular file: %s", path)
		}
		if len(files)+1 >= entryLimit {
			return fmt.Errorf("archive contains too many entries")
		}
		if strings.HasPrefix(filepath.ToSlash(rel), enginepaths.WorkerBranchesDirName+"/") && workerMetadataPath(filepath.ToSlash(rel), false) && kind != fileKindRegular {
			return fmt.Errorf("worker layout metadata must be a regular file")
		}
		files[filepath.ToSlash(rel)] = archiveSource{path: path, kind: kind, mode: info.Mode().Perm()}
		return nil
	})
	if err != nil {
		return fmt.Errorf("%w: walk %s: %w", ErrCaptureIncomplete, relDir, err)
	}
	return nil
}

func writeArchive(ctx context.Context, destPath string, manifest Manifest, files map[string]archiveSource) (string, error) {
	reader, writer := io.Pipe()
	digest := sha256.New()
	streamDone := make(chan error, 1)
	go func() {
		err := streamArchive(ctx, writer, manifest, files)
		_ = writer.CloseWithError(err)
		streamDone <- err
	}()

	_, commitErr := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(destPath),
		Source:   io.TeeReader(reader, digest),
		Mode:     fileMode,
		BeforeCommit: func(target fseffect.Target, _ fseffect.Result) error {
			_, err := target.Lstat()
			switch {
			case err == nil:
				return os.ErrExist
			case errors.Is(err, os.ErrNotExist):
				return nil
			default:
				return err
			}
		},
	})
	if commitErr != nil {
		_ = reader.CloseWithError(commitErr)
	}
	streamErr := <-streamDone
	if streamErr != nil {
		return "", fmt.Errorf("backup: write archive: %w", streamErr)
	}
	if commitErr != nil {
		return "", fmt.Errorf("backup: commit archive: %w", commitErr)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func streamArchive(ctx context.Context, dest io.Writer, manifest Manifest, files map[string]archiveSource) error {
	manifestRaw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := validateCaptureManifest(manifest, manifestRaw); err != nil {
		return err
	}
	zw := zip.NewWriter(&archiveLimitWriter{dest: dest, remaining: MaxArchiveBytes})
	err = writeZipBytes(zw, manifestEntryName, manifestRaw)
	for _, fe := range manifest.Files {
		if err != nil {
			break
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = ctxErr
			break
		}
		err = writeZipSource(ctx, zw, fe, files[fe.RelPath])
	}
	if closeErr := zw.Close(); err == nil {
		err = closeErr
	}
	return err
}

func writeZipBytes(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func writeZipSource(ctx context.Context, zw *zip.Writer, fe FileEntry, source archiveSource) error {
	dest, err := zw.Create(fe.RelPath)
	if err != nil {
		return err
	}
	h := sha256.New()
	if source.kind == fileKindSymlink {
		target, readErr := os.Readlink(source.path)
		if readErr != nil {
			return fmt.Errorf("%w: read %s: %w", ErrCaptureIncomplete, fe.RelPath, readErr)
		}
		n, writeErr := io.Copy(io.MultiWriter(dest, h), strings.NewReader(target))
		if writeErr != nil {
			return writeErr
		}
		if n != fe.Size || hex.EncodeToString(h.Sum(nil)) != fe.SHA256 {
			return fmt.Errorf("%w: %s changed during capture", ErrCaptureIncomplete, fe.RelPath)
		}
		return nil
	}
	src, err := os.Open(source.path)
	if err != nil {
		return fmt.Errorf("%w: read %s: %w", ErrCaptureIncomplete, fe.RelPath, err)
	}
	defer func() { _ = src.Close() }()
	n, err := io.Copy(io.MultiWriter(dest, h), contextReader{ctx: ctx, in: io.LimitReader(src, fe.Size+1)})
	if err != nil {
		return err
	}
	if n != fe.Size || hex.EncodeToString(h.Sum(nil)) != fe.SHA256 {
		return fmt.Errorf("%w: %s changed during capture", ErrCaptureIncomplete, fe.RelPath)
	}
	return nil
}

func sourceDigest(ctx context.Context, source archiveSource) (string, int64, error) {
	if source.kind == fileKindSymlink {
		target, err := os.Readlink(source.path)
		if err != nil {
			return "", 0, err
		}
		return sha256Hex([]byte(target)), int64(len(target)), nil
	}
	f, err := os.Open(source.path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	if info.Size() < 0 || info.Size() == math.MaxInt64 {
		return "", 0, fmt.Errorf("file size cannot be represented")
	}
	h := sha256.New()
	n, err := io.Copy(h, contextReader{ctx: ctx, in: io.LimitReader(f, info.Size()+1)})
	if n != info.Size() {
		return "", 0, fmt.Errorf("file changed during hashing")
	}
	return hex.EncodeToString(h.Sum(nil)), n, err
}

// ArchiveFilename returns the Content-Disposition basename for a manifest.
func ArchiveFilename(m Manifest) string {
	day := "unknown"
	if t, err := time.Parse(time.RFC3339, m.CreatedAt); err == nil {
		day = t.UTC().Format("20060102")
	}
	return fmt.Sprintf("painted-wolf-backup-%s.zip", day)
}

func sha256File(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
