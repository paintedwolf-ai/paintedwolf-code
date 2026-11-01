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
	"github.com/lycaon/lycaon/internal/gitrepo"
)

// Receipt identifies the exact entry returned by the native Trash service or
// placed in a root's private recovery directory. It retains no file contents
// and is unavailable after that entry is removed or replaced.
type Receipt struct {
	FormatVersion int    `json:"format_version"`
	Platform      string `json:"platform"`
	Path          string `json:"path"`
	Identity      string `json:"identity"`
	Metadata      string `json:"metadata,omitempty"`
}

// Destination selects where a moved-aside entry is retained: the operating
// system Trash for human deletions or the root's private recovery directory
// for agent removals and replacements.
type Destination struct {
	system   bool
	source   fseffect.Location
	dir      string
	key      string
	relocate func(fseffect.Location, fseffect.Location, string) error
}

// SystemTrash selects the host operating system Trash.
func SystemTrash() Destination {
	return Destination{system: true}
}

// RecoveryArea selects a host-protected recovery directory on the same
// filesystem as the entry's root.
func RecoveryArea(source fseffect.Location, dir, key string, relocate func(fseffect.Location, fseffect.Location, string) error) Destination {
	return Destination{source: source, dir: dir, key: key, relocate: relocate}
}

var ErrUnavailable = errors.New("the item is no longer available in Trash")

// Move moves path to the operating system Trash; it never falls back to permanent removal.
func Move(ctx context.Context, path string) (Receipt, error) {
	return MoveTo(ctx, path, SystemTrash())
}

// MoveTo moves path to dest (system Trash or agent recovery area) and records
// its destination path and kernel entry identity without reading file contents.
func MoveTo(ctx context.Context, path string, dest Destination) (Receipt, error) {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return Receipt{}, fmt.Errorf("trash requires an absolute path")
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	if !dest.system {
		return moveToRecoveryArea(path, dest)
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

func moveToRecoveryArea(path string, dest Destination) (Receipt, error) {
	if !filepath.IsAbs(dest.dir) || strings.TrimSpace(dest.key) == "" {
		return Receipt{}, fmt.Errorf("recovery area requires an absolute directory and key")
	}
	if err := os.MkdirAll(dest.dir, 0o700); err != nil {
		return Receipt{}, err
	}
	_ = gitrepo.EnsureRecoveryExcluded(dest.source.Root)
	identity, err := fspath.EntryIdentity(path)
	if err != nil {
		return Receipt{}, err
	}
	relocate := dest.relocate
	if relocate == nil {
		relocate = fseffect.RelocateGuarded
	}
	target := fseffect.Location{Root: dest.dir, Rel: dest.key}
	targetPath := filepath.Join(dest.dir, dest.key)
	moveErr := relocate(dest.source, target, identity)
	published, publishedErr := fspath.EntryIdentity(targetPath)
	if publishedErr == nil && published == identity {
		return Receipt{
			FormatVersion: 1,
			Platform:      runtime.GOOS,
			Path:          targetPath,
			Identity:      identity,
		}, moveErr
	}
	if moveErr == nil {
		moveErr = publishedErr
	}
	return Receipt{}, moveErr
}

// Restore consumes only the recorded identity and refuses an occupied destination.
func Restore(ctx context.Context, receipt Receipt, destination fseffect.Location) error {
	return RestoreWith(ctx, receipt, destination, nil)
}

// RestoreWith restores receipt to destination using relocate when provided.
func RestoreWith(ctx context.Context, receipt Receipt, destination fseffect.Location, relocate func(fseffect.Location, fseffect.Location, string) error) error {
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
	if relocate != nil {
		err = relocate(fseffect.Location{Root: filepath.Dir(receipt.Path), Rel: filepath.Base(receipt.Path)}, destination, receipt.Identity)
	} else {
		err = restoreEntry(receipt, destination)
	}
	if err != nil {
		return err
	}
	// Restoration has completed; stale native metadata cannot undo that effect.
	_ = removeTrashMetadata(receipt)
	return nil
}
