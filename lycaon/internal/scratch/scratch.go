// Package scratch owns session scratch: one private working folder per session
// for disposable intermediates such as notes, command output, and throwaway
// scripts.
//
// Every session, coordinator or worker, has its own folder directly under the
// engine's scratch root. Agents reach only their own folder, and only the
// engine writes the root, so no agent can put a link in place of another
// session's folder. Folders are created component by component without
// following links. A folder lives until its chat is deleted or a person
// clears scratch; nothing can rebuild its contents.
package scratch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
)

// ErrInvalidSessionID marks a session id that cannot name one folder.
var ErrInvalidSessionID = errors.New("invalid session id for scratch")

// Folders locates, creates, measures, and removes session scratch folders
// under one engine state root.
type Folders struct {
	stateRoot string
}

// New returns the scratch folders under stateRoot.
func New(stateRoot string) *Folders {
	return &Folders{stateRoot: filepath.Clean(strings.TrimSpace(stateRoot))}
}

// Ensure creates sessionID's folder when it is missing and returns its
// canonical path. It refuses a folder or ancestor below the state root that
// is a link rather than a directory.
func (f *Folders) Ensure(sessionID string) (string, error) {
	rel, err := f.sessionRel(sessionID)
	if err != nil {
		return "", err
	}
	if err := fseffect.MkdirAll(fseffect.Location{Root: f.stateRoot, Rel: rel}, 0o700); err != nil {
		return "", fmt.Errorf("prepare session scratch %q: %w", sessionID, err)
	}
	return filepath.Join(fspath.CanonicalPath(f.stateRoot), rel), nil
}

// Remove deletes the folders of the given sessions. A missing folder is not an error.
func (f *Folders) Remove(sessionIDs ...string) error {
	var errs []error
	for _, id := range sessionIDs {
		rel, err := f.sessionRel(id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := os.RemoveAll(filepath.Join(f.stateRoot, rel)); err != nil {
			errs = append(errs, fmt.Errorf("remove session scratch %q: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

// Hold claims a session so its scratch can be removed. It reports false while
// the session is in use; release ends the claim.
type Hold func(sessionID string) (release func(), ok bool)

// Reclaim removes the folder of every session hold claims and keeps the
// folders of sessions in use. It returns the ids it kept.
func (f *Folders) Reclaim(ctx context.Context, hold Hold) ([]string, error) {
	root := enginepaths.ScratchRootUnder(f.stateRoot)
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list session scratch: %w", err)
	}
	var kept []string
	var errs []error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return kept, err
		}
		name := entry.Name()
		if !entry.IsDir() {
			// Only session folders belong under the root.
			if err := os.Remove(filepath.Join(root, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
			continue
		}
		release, ok := hold(name)
		if !ok {
			kept = append(kept, name)
			continue
		}
		err := os.RemoveAll(filepath.Join(root, name))
		release()
		if err != nil {
			errs = append(errs, fmt.Errorf("remove session scratch %q: %w", name, err))
		}
	}
	return kept, errors.Join(errs...)
}

// Inventory reports whether any session scratch holds a file and the bytes
// its files occupy. Empty folders are not content.
func (f *Folders) Inventory(ctx context.Context) (present bool, bytes int64, err error) {
	root := enginepaths.ScratchRootUnder(f.stateRoot)
	err = filepath.WalkDir(root, func(_ string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		present = true
		info, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		bytes += info.Size()
		return nil
	})
	return present, bytes, err
}

// sessionRel returns sessionID's folder relative to the state root. A session
// id is one path component, never a traversal.
func (f *Folders) sessionRel(sessionID string) (string, error) {
	if f == nil || f.stateRoot == "" || f.stateRoot == "." {
		return "", errors.New("session scratch has no engine state root")
	}
	id := strings.TrimSpace(sessionID)
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\`) || strings.ContainsRune(id, 0) {
		return "", fmt.Errorf("%w: %q", ErrInvalidSessionID, sessionID)
	}
	return filepath.Join(enginepaths.ScratchDirName, id), nil
}
