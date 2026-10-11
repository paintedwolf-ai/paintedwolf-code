// Package desktoptrash moves selected entries through the host operating system trash.
package desktoptrash

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
)

// Receipt identifies the exact entry returned by the native Trash service.
// It retains no file contents and is unavailable after that entry is removed or replaced.
type Receipt struct {
	FormatVersion int    `json:"format_version"`
	Platform      string `json:"platform"`
	Path          string `json:"path"`
	Identity      string `json:"identity"`
	Metadata      string `json:"metadata,omitempty"`
}

var ErrUnavailable = errors.New("the item is no longer available in Trash")

// Move never falls back to permanent removal.
func Move(ctx context.Context, path string) (Receipt, error) {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return Receipt{}, fmt.Errorf("trash requires an absolute path")
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	receipt, moveErr := platformMove(path)
	if receipt.Path == "" {
		if moveErr == nil {
			moveErr = fmt.Errorf("native Trash did not return a recovery location")
		}
		return receipt, moveErr
	}
	receipt.FormatVersion = 1
	receipt.Platform = runtime.GOOS
	identity, identityErr := fspath.EntryIdentity(receipt.Path)
	receipt.Identity = identity
	return receipt, errors.Join(moveErr, identityErr)
}

// Restore consumes only the recorded identity and refuses an occupied destination.
func Restore(ctx context.Context, receipt Receipt, destination fseffect.Location) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if receipt.FormatVersion != 1 || receipt.Platform != runtime.GOOS || !filepath.IsAbs(receipt.Path) || receipt.Identity == "" {
		return ErrUnavailable
	}
	identity, err := fspath.EntryIdentity(receipt.Path)
	if os.IsNotExist(err) {
		return errors.Join(ErrUnavailable, err)
	}
	if err != nil {
		return err
	}
	if identity != receipt.Identity {
		return ErrUnavailable
	}
	if err := restoreEntry(receipt, destination); err != nil {
		return err
	}
	// Restoration has completed; stale native metadata cannot undo that effect.
	_ = removeTrashMetadata(receipt)
	return nil
}
