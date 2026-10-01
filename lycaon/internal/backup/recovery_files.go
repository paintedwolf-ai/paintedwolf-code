package backup

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fileclone"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fssync"
)

func copySnapshotRegular(ctx context.Context, src, dest string, mode os.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	cloned, err := fileclone.Clone(src, dest)
	if err != nil {
		return err
	}
	if cloned {
		if err := os.Chmod(dest, mode.Perm()); err != nil {
			return err
		}
		file, err := os.Open(dest)
		if err != nil {
			return err
		}
		err = fssync.File(file)
		closeErr := file.Close()
		if err != nil {
			return err
		}
		return closeErr
	}
	return copySnapshotStream(ctx, src, dest, mode)
}

func copySnapshotStream(ctx context.Context, src, dest string, mode os.FileMode) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	sourceSize := info.Size()
	if !info.Mode().IsRegular() || sourceSize < 0 || sourceSize == math.MaxInt64 {
		return fmt.Errorf("recovery source is not a regular file")
	}
	available, known, err := availableUpgradeBytes(filepath.Dir(dest))
	if err != nil {
		return err
	}
	if known {
		if err := requireUpgradeSpace(uint64(sourceSize), available); err != nil {
			return err
		}
	}
	file, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(info, opened) || info.Size() != opened.Size() || !info.ModTime().Equal(opened.ModTime()) {
		return fmt.Errorf("%w: recovery source changed before copy", ErrCaptureIncomplete)
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{Location: fseffect.PathLocation(dest), Source: contextReader{ctx: ctx, in: io.LimitReader(file, info.Size()+1)}, Mode: mode.Perm(), BeforeCommit: func(target fseffect.Target, result fseffect.Result) error {
		after, err := file.Stat()
		if err != nil {
			return err
		}
		if result.Bytes != info.Size() || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
			return fmt.Errorf("%w: recovery source changed during copy", ErrCaptureIncomplete)
		}
		_, statErr := target.Lstat()
		if statErr == nil {
			return os.ErrExist
		}
		if !os.IsNotExist(statErr) {
			return statErr
		}
		return nil
	}})
	return err
}

// Snapshot symlinks store their target as regular-file content.
func copySnapshotSource(ctx context.Context, source archiveSource, dest string) error {
	if source.kind == fileKindRegular {
		return copySnapshotRegular(ctx, source.path, dest, source.mode)
	}
	target, err := os.Readlink(source.path)
	if err != nil {
		return err
	}
	if len(target) == 0 || int64(len(target)) > maxSymlinkTargetBytes {
		return fmt.Errorf("invalid recovery symlink target")
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{Location: fseffect.PathLocation(dest), Source: strings.NewReader(target), Mode: fileMode, DirMode: 0o700})
	return err
}

func snapshotFile(root, rel string) (string, os.FileInfo, error) {
	var err error
	root, err = filepath.Abs(root)
	if err != nil {
		return "", nil, err
	}
	path, err := joinUnder(root, rel)
	if err != nil {
		return "", nil, err
	}
	current := path
	for {
		info, err := os.Lstat(current)
		if err != nil {
			return "", nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", nil, fmt.Errorf("recovery path contains a symlink")
		}
		if current == path && !info.Mode().IsRegular() {
			return "", nil, fmt.Errorf("recovery file is not regular")
		}
		if current != path && !info.IsDir() {
			return "", nil, fmt.Errorf("recovery parent is not a directory")
		}
		if current == root {
			break
		}
		current = filepath.Dir(current)
	}
	info, err := os.Lstat(path)
	return path, info, err
}

func verifySnapshotFile(ctx context.Context, root string, entry FileEntry) error {
	path, _, err := snapshotFile(root, entry.RelPath)
	if err != nil {
		return err
	}
	sum, size, err := sourceDigest(ctx, archiveSource{path: path, kind: fileKindRegular})
	if err != nil {
		return err
	}
	if sum != entry.SHA256 || size != entry.Size {
		return &InvalidError{Detail: "recovery file failed verification: " + entry.RelPath}
	}
	return nil
}

func recoveryCloneAvailable(root string) (bool, error) {
	source, err := os.CreateTemp(root, ".recovery-clone-probe-")
	if err != nil {
		return false, err
	}
	path := source.Name()
	defer func() { _ = os.Remove(path); _ = os.Remove(path + ".clone") }()
	probe := make([]byte, 4096)
	probe[0] = 1
	if _, err := source.Write(probe); err != nil {
		_ = source.Close()
		return false, err
	}
	if err := fssync.File(source); err != nil {
		_ = source.Close()
		return false, err
	}
	if err := source.Close(); err != nil {
		return false, err
	}
	return fileclone.Clone(path, path+".clone")
}
