// Package scratch owns session work areas: writable intermediates and host-owned
// command output copies. Both live until chat deletion or idle-session cleanup.
package scratch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
)

// ErrInvalidSessionID marks a session id that cannot name one folder.
var ErrInvalidSessionID = errors.New("invalid session id")

// Folders locates, creates, measures, and removes both session work areas
// under one engine state root.
type Folders struct {
	stateRoot string
}

// New locates session work areas under stateRoot.
func New(stateRoot string) *Folders {
	return &Folders{stateRoot: filepath.Clean(strings.TrimSpace(stateRoot))}
}

// Directories carries distinct scratch and output capabilities. Preparing
// Output adds no command write authority.
type Directories struct{ Scratch, Output string }

// Ensure prepares both folders beneath the host state root without following
// links. No session capability is returned when either preparation fails.
func (f *Folders) Ensure(sessionID string) (Directories, error) {
	id, err := f.sessionID(sessionID)
	if err != nil {
		return Directories{}, err
	}
	dirs := Directories{Scratch: enginepaths.SessionScratchUnder(fspath.CanonicalPath(f.stateRoot), id), Output: enginepaths.SessionOutputUnder(fspath.CanonicalPath(f.stateRoot), id)}
	for _, root := range []string{enginepaths.ScratchDirName, enginepaths.SessionOutputDirName} {
		if _, err := fseffect.MkdirAll(fseffect.Location{Root: f.stateRoot, Rel: filepath.Join(root, id)}, 0700); err != nil {
			return Directories{}, fmt.Errorf("prepare session folder %q: %w", id, err)
		}
	}
	return dirs, nil
}

// Remove deletes the folders of the given sessions. A missing folder is not an error.
func (f *Folders) Remove(sessionIDs ...string) error {
	var errs []error
	for _, session := range sessionIDs {
		id, err := f.sessionID(session)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, root := range []string{enginepaths.ScratchRootUnder(f.stateRoot), enginepaths.SessionOutputRootUnder(f.stateRoot)} {
			if err := os.RemoveAll(filepath.Join(root, id)); err != nil {
				errs = append(errs, fmt.Errorf("remove session folder %q: %w", id, err))
			}
		}
	}
	return errors.Join(errs...)
}

// Hold claims a session so its work areas can be removed. It reports false while
// the session is in use; release ends the claim.
type Hold func(sessionID string) (release func(), ok bool)

// Reclaim removes the folder of every session hold claims and keeps the
// folders of sessions in use. It returns the ids it kept.
func (f *Folders) Reclaim(ctx context.Context, hold Hold) ([]string, error) {
	names := map[string]bool{}
	for _, root := range []string{enginepaths.ScratchRootUnder(f.stateRoot), enginepaths.SessionOutputRootUnder(f.stateRoot)} {
		entries, err := os.ReadDir(root)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("list session folders: %w", err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				if err := os.Remove(filepath.Join(root, entry.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return nil, err
				}
				continue
			}
			names[entry.Name()] = true
		}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	var kept []string
	var errs []error
	for _, name := range ordered {
		if err := ctx.Err(); err != nil {
			return kept, err
		}
		release, ok := hold(name)
		if !ok {
			kept = append(kept, name)
			continue
		}
		err := f.Remove(name)
		release()
		if err != nil {
			errs = append(errs, err)
		}
	}
	return kept, errors.Join(errs...)
}

// Inventory reports whether any session work area holds a file and the bytes
// its files occupy. Empty folders are not content.
func (f *Folders) Inventory(ctx context.Context) (present bool, bytes int64, err error) {
	for _, root := range []string{enginepaths.ScratchRootUnder(f.stateRoot), enginepaths.SessionOutputRootUnder(f.stateRoot)} {
		err = filepath.WalkDir(root, func(_ string, d fs.DirEntry, walkErr error) error {
			if errors.Is(walkErr, fs.ErrNotExist) {
				return nil
			}
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			info, err := d.Info()
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			present = true
			bytes += info.Size()
			return nil
		})
		if err != nil {
			return present, bytes, err
		}
	}

	return present, bytes, err
}

// sessionID validates one exact child name for both session areas.
func (f *Folders) sessionID(sessionID string) (string, error) {
	if f == nil || f.stateRoot == "" || f.stateRoot == "." {
		return "", errors.New("session folders have no engine state root")
	}
	id := strings.TrimSpace(sessionID)
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\`) || strings.ContainsRune(id, 0) {
		return "", fmt.Errorf("%w: %q", ErrInvalidSessionID, sessionID)
	}
	return id, nil
}
