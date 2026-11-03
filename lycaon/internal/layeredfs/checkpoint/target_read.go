package checkpoint

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"syscall"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

func (pr *Promoter) openTargetRoot(id string) (*os.Root, error) {
	name, ok := pr.cfg.TargetRoots[id]
	if !ok {
		return nil, layeredfs.ErrInvalidRoot
	}
	before, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() {
		return nil, layeredfs.ErrInvalidRoot
	}
	root, err := os.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	held, err := root.Stat(".")
	if err != nil || !os.SameFile(before, held) {
		_ = root.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrStalePreviewTargetChanged
	}
	return root, nil
}

func (pr *Promoter) observeTarget(qpath layeredfs.QualifiedPath) (TargetObservation, error) {
	root, err := pr.openTargetRoot(qpath.RootID)
	if err != nil {
		return TargetObservation{}, err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(qpath.Rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return TargetObservation{Path: qpath, Kind: layeredfs.EntryAbsent}, nil
		}
		return TargetObservation{}, err
	}
	obs := TargetObservation{Path: qpath, Mode: info.Mode(), Size: info.Size(), ModTime: info.ModTime()}
	if info.Mode()&fs.ModeSymlink != 0 {
		obs.Kind = layeredfs.EntrySymlink
		obs.LinkTarget, err = root.Readlink(qpath.Rel)
		return obs, err
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		obs.Kind = layeredfs.EntryAbsent
		return obs, nil
	}
	file, err := root.Open(qpath.Rel)
	if err != nil {
		return TargetObservation{}, err
	}
	defer func() { _ = file.Close() }()
	held, err := file.Stat()
	if err != nil {
		return TargetObservation{}, err
	}
	if !os.SameFile(info, held) {
		return TargetObservation{}, ErrStalePreviewTargetChanged
	}
	obs.Mode, obs.Size, obs.ModTime = held.Mode(), held.Size(), held.ModTime()
	if held.IsDir() {
		obs.Kind = layeredfs.EntryDirectory
		entries, readErr := file.ReadDir(1)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return TargetObservation{}, readErr
		}
		obs.DirEmpty = len(entries) == 0
		return obs, nil
	}
	obs.Kind = layeredfs.EntryRegular
	body, err := io.ReadAll(file)
	if err != nil {
		return TargetObservation{}, err
	}
	obs.SHA256 = sha256Hex(body)
	return obs, nil
}

func (pr *Promoter) readObservedTarget(observed TargetObservation) ([]byte, error) {
	root, err := pr.openTargetRoot(observed.Path.RootID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	body, err := root.ReadFile(observed.Path.Rel)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) != observed.Size || sha256Hex(body) != observed.SHA256 {
		return nil, ErrStalePreviewTargetChanged
	}
	return body, nil
}
