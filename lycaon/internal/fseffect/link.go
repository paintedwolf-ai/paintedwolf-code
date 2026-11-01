package fseffect

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type rootLinkTarget struct {
	root *os.Root
	name string
}

func (t rootLinkTarget) Open() (*os.File, error)     { return t.root.Open(t.name) }
func (t rootLinkTarget) Lstat() (os.FileInfo, error) { return t.root.Lstat(t.name) }

func platformLink(req LinkRequest) error {
	loc, err := cleanLocation(req.Location)
	if err != nil {
		return err
	}
	if req.Target == "" {
		return fmt.Errorf("symbolic link target is empty")
	}
	unlock := lockTarget(loc)
	defer unlock()
	root, err := os.OpenRoot(loc.Root)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	parentRel, name := filepath.Split(loc.Rel)
	parent, err := root.OpenRoot(filepath.Clean(parentRel))
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	target := rootLinkTarget{root: parent, name: name}
	if _, err := parent.Lstat(name); err == nil && !req.Replace {
		return os.ErrExist
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	tmp := ".paintedwolf-link-" + hex.EncodeToString(random[:])
	if err := parent.Symlink(req.Target, tmp); err != nil {
		return err
	}
	defer func() { _ = parent.Remove(tmp) }()
	if req.BeforeCommit != nil {
		if err := req.BeforeCommit(target); err != nil {
			return err
		}
	}
	if _, err := parent.Lstat(name); err == nil && !req.Replace {
		return os.ErrExist
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := parent.Rename(tmp, name); err != nil {
		return err
	}
	return SyncDirectory(root, filepath.Clean(parentRel))
}
