package checkpoint

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"syscall"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// openTargetRoot opens a target root and checks it is the directory its path names. A transaction
// holding its roots opens and checks each once.
func (tree *promotionTargets) openTargetRoot(id string) (*targetRoot, error) {
	tree.mu.Lock()
	defer tree.mu.Unlock()
	if held := tree.held[id]; held != nil {
		return held, nil
	}
	name, ok := tree.roots[id]
	if !ok {
		return nil, layeredfs.ErrInvalidRoot
	}
	tree.ops.add("open_root")
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
	opened := &targetRoot{root: root, ops: tree.ops}
	if tree.held != nil {
		opened.held = true
		tree.held[id] = opened
	}
	return opened, nil
}

// holdRoots keeps each target root open from its first use until the matching releaseRoots. Holds
// nest, so overlapping passes share the roots and the last release closes them.
func (tree *promotionTargets) holdRoots() {
	tree.mu.Lock()
	defer tree.mu.Unlock()
	if tree.holds == 0 {
		tree.held = map[string]*targetRoot{}
	}
	tree.holds++
}

func (tree *promotionTargets) releaseRoots() {
	tree.mu.Lock()
	defer tree.mu.Unlock()
	if tree.holds == 0 {
		return
	}
	if tree.holds--; tree.holds > 0 {
		return
	}
	for _, root := range tree.held {
		_ = root.root.Close()
	}
	tree.held = nil
}

func (tree *promotionTargets) observeTarget(qpath layeredfs.QualifiedPath) (TargetObservation, error) {
	root, err := tree.openTargetRoot(qpath.RootID)
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
		return TargetObservation{}, fmt.Errorf("checkpoint: unsupported target entry type %s", info.Mode())
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
		entries, readErr := file.ReadDir(-1)
		if readErr != nil {
			return TargetObservation{}, readErr
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name()+"\x00"+entry.Type().String())
		}
		slices.Sort(names)
		raw, err := json.Marshal(names)
		if err != nil {
			return TargetObservation{}, err
		}
		obs.DirectorySHA256 = sha256Hex(raw)
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

func (tree *promotionTargets) readObservedTarget(observed TargetObservation) ([]byte, error) {
	root, err := tree.openTargetRoot(observed.Path.RootID)
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

// TargetFileLinks reports how many names share the regular file at path.
func TargetFileLinks(path string) (uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	_, links, err := targetFileIdentityAndLinks(file, info)
	return links, err
}
