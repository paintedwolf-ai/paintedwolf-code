package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/editoroutbox"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/localdata"
)

func captureRecoveryDirectory(ctx context.Context, opts CreateOpts, destination string) (Manifest, error) {
	release := retainCaptureBodies(opts.ConfigDir)
	defer release()
	if err := os.Mkdir(destination, 0o700); err != nil {
		return Manifest{}, err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(destination)
		}
	}()
	if err := captureEditorOutbox(ctx, opts.ConfigDir, destination); err != nil {
		return Manifest{}, err
	}
	storePath := filepath.Join(destination, storeRelPath)
	if err := db.CreateSnapshot(ctx, opts.SQLDB, storePath); err != nil {
		return Manifest{}, err
	}
	source, err := db.OpenReadOnly(ctx, storePath)
	if err != nil {
		return Manifest{}, err
	}
	shape, err := db.ShapeDigest(ctx, source)
	_ = source.Close()
	if err != nil {
		return Manifest{}, err
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	manifest := Manifest{FormatVersion: FormatVersion, AppVersion: opts.AppVersion, SchemaUserVersion: opts.SchemaUserVersion, SchemaShapeDigest: shape, CreatedAt: now.UTC().Format(time.RFC3339Nano), ReplaceRelPaths: localdata.BackupRelPaths(), ReplaceRelDirs: localdata.BackupRelDirs()}
	files := map[string]archiveSource{storeRelPath: {path: storePath, kind: fileKindRegular, mode: fileMode}}
	for _, rel := range manifest.ReplaceRelPaths {
		if rel == storeRelPath {
			continue
		}
		path := filepath.Join(opts.ConfigDir, filepath.FromSlash(rel))
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return Manifest{}, err
		}
		if !info.Mode().IsRegular() {
			return Manifest{}, fmt.Errorf("durable path is not a regular file: %s", rel)
		}
		files[rel] = archiveSource{path: path, kind: fileKindRegular, mode: info.Mode().Perm()}
	}
	branches := branchCapture{all: true, roots: map[string]bool{}}
	for _, dir := range manifest.ReplaceRelDirs {
		base := opts.ConfigDir
		if dir == editoroutbox.Directory() {
			base = destination
		}
		if err := collectDurableDir(ctx, base, dir, files, branches, math.MaxInt); err != nil {
			return Manifest{}, err
		}
	}
	manifest.RetainedBranchTrees = branches.names()
	for _, root := range manifest.RetainedBranchTrees {
		if err := os.MkdirAll(filepath.Join(destination, filepath.FromSlash(root)), 0o700); err != nil {
			return Manifest{}, err
		}
		if err := syncDirectoryTree(destination, filepath.Join(destination, filepath.FromSlash(root))); err != nil {
			return Manifest{}, err
		}
	}
	for rel, file := range files {
		dest := filepath.Join(destination, filepath.FromSlash(rel))
		if rel != storeRelPath && file.path != dest {
			if err := copySnapshotSource(ctx, file, dest); err != nil {
				return Manifest{}, err
			}
		}
		sum, size, err := sourceDigest(ctx, archiveSource{path: dest, kind: fileKindRegular})
		if err != nil {
			return Manifest{}, err
		}
		manifest.Files = append(manifest.Files, FileEntry{RelPath: rel, Kind: file.kind, Mode: uint32(file.mode.Perm()), Size: size, SHA256: sum})
		if err := syncDirectoryTree(destination, filepath.Dir(dest)); err != nil {
			return Manifest{}, err
		}
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].RelPath < manifest.Files[j].RelPath })
	sizes := make(map[string]uint64, len(manifest.Files))
	for _, entry := range manifest.Files {
		if entry.Size < 0 {
			return Manifest{}, fmt.Errorf("invalid captured file size")
		}
		sizes[entry.RelPath] = uint64(entry.Size)
	}
	if err := validateManifestEntries(manifest, localdata.BackupRelPaths(), localdata.BackupRelDirs(), sizes); err != nil {
		return Manifest{}, err
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Manifest{}, err
	}
	if _, err := fseffect.Replace(fseffect.ReplaceRequest{Location: fseffect.PathLocation(filepath.Join(destination, manifestEntryName)), Source: strings.NewReader(string(raw)), Mode: fileMode}); err != nil {
		return Manifest{}, err
	}
	if err := syncDirectoryTree(filepath.Dir(destination), destination); err != nil {
		return Manifest{}, err
	}
	complete = true
	return manifest, nil
}
