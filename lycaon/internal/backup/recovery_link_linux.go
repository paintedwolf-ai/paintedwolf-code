//go:build linux

package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"golang.org/x/sys/unix"
)

func immutableRecoveryBody(rel string, info os.FileInfo) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o400 || len(parts) != 3 || parts[0] != enginepaths.SourceContentDirName || !strings.HasSuffix(parts[2], ".zst") {
		return false
	}
	object, err := sourceblob.RelPath(parts[1] + strings.TrimSuffix(parts[2], ".zst"))
	return err == nil && filepath.ToSlash(object) == strings.Join(parts[1:], "/")
}

func linkSnapshotBody(source archiveSource, dest string) (bool, error) {
	if !source.shareBody {
		return false, nil
	}
	before, err := os.Lstat(source.path)
	if err != nil {
		return false, err
	}
	if !before.Mode().IsRegular() || before.Mode().Perm() != 0o400 {
		return false, fmt.Errorf("%w: immutable recovery body changed", ErrCaptureIncomplete)
	}
	if err := os.Link(source.path, dest); err != nil {
		if errors.Is(err, unix.EXDEV) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.EPERM) || errors.Is(err, unix.EMLINK) {
			return false, nil
		}
		return false, err
	}
	after, err := os.Lstat(dest)
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() || after.Mode().Perm() != 0o400 || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		_ = os.Remove(dest)
		return false, fmt.Errorf("%w: immutable recovery body changed while linking", ErrCaptureIncomplete)
	}
	return true, nil
}

func sharedRecoveryBodyOnVolume(root string, source archiveSource) bool {
	if !source.shareBody {
		return false
	}
	var volume, body unix.Stat_t
	return unix.Stat(root, &volume) == nil && unix.Lstat(source.path, &body) == nil && volume.Dev == body.Dev
}

func recoveryBodyLinksAvailable(root string) (bool, error) {
	probe, err := os.CreateTemp(root, ".recovery-link-probe-")
	if err != nil {
		return false, err
	}
	path := probe.Name()
	defer func() { _ = os.Remove(path); _ = os.Remove(path + ".link") }()
	if err := probe.Chmod(0o400); err != nil {
		_ = probe.Close()
		return false, err
	}
	if err := probe.Close(); err != nil {
		return false, err
	}
	return linkSnapshotBody(archiveSource{path: path, mode: 0o400, shareBody: true}, path+".link")
}
