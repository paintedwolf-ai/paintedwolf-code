//go:build !darwin

package desktoptrash

import (
	"github.com/lycaon/lycaon/internal/fseffect"
	"path/filepath"
)

func restoreEntry(receipt Receipt, destination fseffect.Location) error {
	return fseffect.RelocateGuarded(fseffect.Location{Root: filepath.Dir(receipt.Path), Rel: filepath.Base(receipt.Path)}, destination, receipt.Identity)
}
