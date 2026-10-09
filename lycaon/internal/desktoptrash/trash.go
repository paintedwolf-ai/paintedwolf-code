// Package desktoptrash moves selected entries through the host operating system trash.
package desktoptrash

import (
 "context"
 "errors"
 "fmt"
 "path/filepath"
 "runtime"
 "strings"

 "github.com/lycaon/lycaon/internal/fseffect"
 "github.com/lycaon/lycaon/internal/fspath"
)

// Receipt identifies the exact entry returned by the native Trash service.
// It retains no file contents and is unavailable after that entry is removed or replaced.
type Receipt struct {
 Platform string `json:"platform"`
 Path string `json:"path"`
 Identity string `json:"identity"`
 Metadata string `json:"metadata,omitempty"`
}

var ErrUnavailable = errors.New("the item is no longer available in Trash")

// Move never falls back to permanent removal.
func Move(ctx context.Context, path string) (Receipt, error) {
 if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) { return Receipt{}, fmt.Errorf("trash requires an absolute path") }
 if err := ctx.Err(); err != nil { return Receipt{}, err }
 receipt, err := platformMove(path)
 if err != nil { return receipt, err }
 receipt.Platform = runtime.GOOS
 receipt.Identity, err = fspath.EntryIdentity(receipt.Path)
 return receipt, err
}

// Restore consumes only the recorded identity and refuses an occupied destination.
func Restore(ctx context.Context, receipt Receipt, destination fseffect.Location) error {
 if err := ctx.Err(); err != nil { return err }
 if receipt.Platform != runtime.GOOS || !filepath.IsAbs(receipt.Path) || receipt.Identity == "" { return ErrUnavailable }
 identity, err := fspath.EntryIdentity(receipt.Path)
 if err != nil || identity != receipt.Identity { return errors.Join(ErrUnavailable, err) }
 if err := fseffect.RelocateGuarded(fseffect.Location{Root: filepath.Dir(receipt.Path), Rel: filepath.Base(receipt.Path)}, destination, receipt.Identity); err != nil { return err }
 return removeTrashMetadata(receipt)
}
