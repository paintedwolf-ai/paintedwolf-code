//go:build darwin

package desktoptrash

import "github.com/lycaon/lycaon/internal/fseffect"

func restoreEntry(receipt Receipt, destination fseffect.Location) error {
	return fseffect.RestoreNativeTrash(receipt.Path, destination, receipt.Identity)
}
