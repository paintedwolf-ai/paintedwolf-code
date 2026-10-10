package backup

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fssync"
)

func ensureDirectoryPath(root, leaf string) error {
	root = filepath.Clean(root)
	rel, err := filepath.Rel(root, filepath.Clean(leaf))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("directory %s is outside restore root %s", leaf, root)
	}
	current := root
	if rel == "." {
		return nil
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		switch {
		case os.IsNotExist(statErr):
			if err := os.Mkdir(current, 0o700); err != nil {
				return err
			}
			if err := syncDirectory(filepath.Dir(current)); err != nil {
				return err
			}
		case statErr != nil:
			return statErr
		case info.IsDir() && info.Mode()&os.ModeSymlink == 0:
			continue
		default:
			if err := os.RemoveAll(current); err != nil {
				return err
			}
			if err := os.Mkdir(current, 0o700); err != nil {
				return err
			}
			if err := syncDirectory(filepath.Dir(current)); err != nil {
				return err
			}
		}
	}
	return nil
}

// clearSQLiteSidecars removes journals beside an installed database.
func clearSQLiteSidecars(dest string, mainStore bool) error {
	if !mainStore && !strings.HasSuffix(strings.ToLower(filepath.Base(dest)), ".db") {
		return nil
	}
	if err := db.RemoveStoreSidecars(dest); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(dest))
}

// installFileAtomic installs the entry and syncs directories through the restore root.
func installFileAtomic(ctx context.Context, root, src, dest string, entry PendingFile) error {
	if entry.Kind == fileKindSymlink {
		return installSymlinkAtomic(root, src, dest, entry.RelPath)
	}
	tmp := filepath.Join(filepath.Dir(dest), ".restore-file-"+uuid.NewString())
	defer func() { _ = os.Remove(tmp) }()
	if _, err := copySnapshotRegular(ctx, src, tmp, os.FileMode(entry.Mode)); err != nil {
		return fmt.Errorf("copy staged file: %w", err)
	}
	tempRel, err := filepath.Rel(root, tmp)
	if err != nil {
		return err
	}
	destRel, err := filepath.Rel(root, dest)
	if err != nil {
		return err
	}
	if err := fseffect.Rename(root, tempRel, destRel); err != nil {
		return fmt.Errorf("install staged file: %w", err)
	}

	return syncDirectoryTree(root, filepath.Dir(dest))
}

func installSymlinkAtomic(root, src, dest, rel string) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read staged symlink: %w", err)
	}
	if len(raw) == 0 || int64(len(raw)) > maxSymlinkTargetBytes || bytes.IndexByte(raw, 0) >= 0 {
		return fmt.Errorf("staged symlink target is invalid")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return fmt.Errorf("mkdir symlink destination: %w", err)
	}
	tmpRel := filepath.ToSlash(filepath.Join(filepath.Dir(rel), ".restore-link-"+uuid.NewString()))
	tmp, err := joinUnder(root, tmpRel)
	if err != nil {
		return err
	}
	if err := os.Symlink(string(raw), tmp); err != nil {
		return fmt.Errorf("stage symlink: %w", err)
	}
	defer func() { _ = os.Remove(tmp) }()
	if err := fseffect.Rename(root, filepath.FromSlash(tmpRel), filepath.FromSlash(rel)); err != nil {
		return fmt.Errorf("install staged symlink: %w", err)
	}
	return syncDirectoryTree(root, filepath.Dir(dest))
}

func installedDigest(path, kind string) (string, int64, error) {
	if kind == fileKindSymlink {
		target, err := os.Readlink(path)
		if err != nil {
			return "", 0, err
		}
		return sha256Hex([]byte(target)), int64(len(target)), nil
	}
	return sha256File(path)
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return fssync.File(dir)
}

func syncDirectoryTree(root, leaf string) error {
	root = filepath.Clean(root)
	current := filepath.Clean(leaf)
	rel, err := filepath.Rel(root, current)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("directory %s is outside restore root %s", leaf, root)
	}
	for {
		if err := syncDirectory(current); err != nil {
			return err
		}
		if current == root {
			return nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return fmt.Errorf("directory %s is outside restore root %s", leaf, root)
		}
		rel, err := filepath.Rel(root, parent)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("directory %s is outside restore root %s", leaf, root)
		}
		current = parent
	}
}
