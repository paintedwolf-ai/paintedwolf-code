// Package desktoptrash moves selected entries through the host operating system trash.
package desktoptrash

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// Move moves a file or directory to the system trash or recycle bin.
// It never falls back to permanent removal.
func Move(ctx context.Context, path string) error {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return fmt.Errorf("trash requires an absolute path")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return platformMove(path)
}
