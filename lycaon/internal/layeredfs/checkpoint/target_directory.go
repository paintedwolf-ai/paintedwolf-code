package checkpoint

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

func (tree *promotionTargets) ensureTargetParents(root *os.Root, path layeredfs.QualifiedPath) error {
	var missing []string
	for parent := filepath.Dir(path.Rel); parent != "."; parent = filepath.Dir(parent) {
		info, err := root.Stat(parent)
		if err == nil {
			if !info.IsDir() {
				return ErrStalePreviewTargetChanged
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		missing = append(missing, parent)
	}
	for index := len(missing) - 1; index >= 0; index-- {
		parent := layeredfs.QualifiedPath{RootID: path.RootID, Rel: missing[index]}
		if err := tree.makeTargetDirectory(root, parent, 0755, nil); err != nil {
			return err
		}
	}
	return nil
}

func (tree *promotionTargets) makeTargetDirectory(root *os.Root, path layeredfs.QualifiedPath, mode os.FileMode, attrs map[string][]byte) error {
	if err := tree.ensureTargetParents(root, path); err != nil {
		return err
	}
	if info, err := root.Lstat(path.Rel); err == nil {
		if !info.IsDir() {
			return ErrStalePreviewTargetChanged
		}
		file, err := root.Open(path.Rel)
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()
		// Existing directories retain unmodified attributes and children.
		if err := file.Chmod(mode); err != nil {
			return err
		}
		if err := applyFileXattrs(file, attrs); err != nil {
			return err
		}
		if tree.recordPostimage != nil {
			if err := tree.recordPostimage(path, file); err != nil {
				return err
			}
		}
		return syncFile(file)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temp := filepath.Join(filepath.Dir(path.Rel), ".checkpoint-"+hex.EncodeToString(nonce[:]))
	if err := root.Mkdir(temp, mode); err != nil {
		return err
	}
	file, err := root.Open(temp)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	identity, err := file.Stat()
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			if current, err := root.Lstat(temp); err == nil && os.SameFile(identity, current) {
				_ = root.Remove(temp)
			}
		}
	}()
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if err := applyFileXattrs(file, attrs); err != nil {
		return err
	}
	if err := syncFile(file); err != nil {
		return err
	}
	if tree.recordPostimage != nil {
		if err := tree.recordPostimage(path, file); err != nil {
			return err
		}
	}
	if _, err := root.Lstat(path.Rel); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return err
		}
		return ErrStalePreviewTargetChanged
	}
	if err := root.Rename(temp, path.Rel); err != nil {
		return err
	}
	published = true
	parent, err := root.Open(filepath.Dir(path.Rel))
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	return syncFile(parent)
}
