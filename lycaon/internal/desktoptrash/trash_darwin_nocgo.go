//go:build darwin && !cgo

package desktoptrash

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func platformMove(path string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve user home for trash: %w", err)
	}
	trashDir := filepath.Join(home, ".Trash")
	if err := os.MkdirAll(trashDir, 0o700); err != nil {
		return fmt.Errorf("create trash directory: %w", err)
	}

	base := filepath.Base(path)
	dest := filepath.Join(trashDir, base)
	if _, err := os.Lstat(dest); err == nil {
		dest = filepath.Join(trashDir, fmt.Sprintf("%s.%d", base, time.Now().UnixNano()))
	}

	return os.Rename(path, dest)
}
