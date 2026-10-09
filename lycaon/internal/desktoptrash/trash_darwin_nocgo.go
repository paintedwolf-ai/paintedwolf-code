//go:build darwin && !cgo

package desktoptrash

import (
	"fmt"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"os"
	"path/filepath"
)

func platformMove(path string) (Receipt, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Receipt{}, fmt.Errorf("resolve user home for trash: %w", err)
	}
	trashDir := filepath.Join(home, ".Trash")
	if err := os.MkdirAll(trashDir, 0o700); err != nil {
		return Receipt{}, fmt.Errorf("create trash directory: %w", err)
	}

	name := filepath.Base(path) + "." + uuid.NewString()
	identity, err := fspath.EntryIdentity(path)
	if err != nil {
		return Receipt{}, err
	}
	destination := filepath.Join(trashDir, name)
	err = fseffect.RelocateGuarded(fseffect.Location{Root: filepath.Dir(path), Rel: filepath.Base(path)}, fseffect.Location{Root: trashDir, Rel: name}, identity)
	return Receipt{Path: destination}, err
}
