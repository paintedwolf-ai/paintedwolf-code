// Package fseffect applies changes through verified directory descriptors.
package fseffect

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	// ErrInvalidPath marks an absolute, empty, or traversing relative path.
	ErrInvalidPath = errors.New("filesystem effect path is invalid")
	// ErrSymlink marks a path component that would require symlink traversal.
	// Reads follow links only while every hop remains inside the root.
	ErrSymlink = errors.New("filesystem effect refuses symlink traversal")
	// ErrPostcondition means committed bytes differ from the staged stream.
	ErrPostcondition = errors.New("filesystem effect postcondition failed")
	// ErrUnsupported means the host lacks the descriptor-relative implementation.
	ErrUnsupported = errors.New("filesystem effect unsupported on this platform")
)

// Location pairs a root capability with a relative target.
type Location struct {
	Root string
	Rel  string
}

// Result identifies the byte stream staged and committed by Replace.
type Result struct {
	Bytes  int64
	SHA256 string
}

// Target exposes the destination through its held parent.
type Target interface {
	Open() (*os.File, error)
	Lstat() (os.FileInfo, error)
}

// ReplaceRequest describes one guarded atomic replacement.
type ReplaceRequest struct {
	Location
	Source io.Reader
	Mode   os.FileMode
	// PreserveMode resolves the destination mode under the target lock.
	PreserveMode bool
	// DirMode applies to created parents; zero uses defaultDirMode.
	DirMode os.FileMode
	// ReviewStaged may await user input without holding the destination lock.
	ReviewStaged func(Target, Result) error
	// BeforeCommit revalidates the destination under the lock.
	BeforeCommit func(Target, Result) error
}

// RemoveRequest describes a descriptor-relative deletion.
// BeforeCommit verifies the selected regular file.
type RemoveRequest struct {
	Location
	BeforeCommit func(Target) error
}

// ModeUpdateRequest describes a guarded permission change on one held file.
type ModeUpdateRequest struct {
	Location
	Update       func(os.FileMode) os.FileMode
	BeforeCommit func(*os.File, os.FileInfo) error
}

// ModeUpdateResult reports the permission transition applied to the held file.
type ModeUpdateResult struct {
	Before os.FileMode
	After  os.FileMode
}

func resolveModeUpdate(
	current os.FileMode,
	update func(os.FileMode) os.FileMode,
) (ModeUpdateResult, os.FileMode) {
	before := current.Perm()
	after := before
	if update != nil {
		after = update(before).Perm()
	}
	special := current & (os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
	return ModeUpdateResult{Before: before, After: after}, after | special
}

func chmodMode(mode os.FileMode) os.FileMode {
	return mode.Perm() | mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky)
}

func (r ReplaceRequest) resolvedMode(target Target) (os.FileMode, error) {
	if !r.PreserveMode {
		return r.Mode, nil
	}
	info, err := target.Lstat()
	if os.IsNotExist(err) {
		return r.Mode, nil
	}
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return r.Mode, nil
	}
	return chmodMode(info.Mode()), nil
}

// defaultDirMode suits tree-visible directories; private state passes 0o700.
const defaultDirMode os.FileMode = 0o755

func (r ReplaceRequest) dirMode() os.FileMode {
	if r.DirMode == 0 {
		return defaultDirMode
	}
	return r.DirMode.Perm()
}

func (r ReplaceRequest) precondition(target Target, staged Result) error {
	if r.BeforeCommit == nil {
		return nil
	}
	return r.BeforeCommit(target, staged)
}

// targetLocks serialize guarded effects on one destination.
var (
	targetLocksMu sync.Mutex
	targetLocks   = map[string]*targetLock{}
)

type targetLock struct {
	mu   sync.Mutex
	refs int
}

func targetLockKey(loc Location) string {
	return filepath.Join(
		filepath.Clean(loc.Root),
		filepath.Clean(loc.Rel),
	)
}

func lockTarget(loc Location) func() {
	key := targetLockKey(loc)
	targetLocksMu.Lock()
	l := targetLocks[key]
	if l == nil {
		l = &targetLock{}
		targetLocks[key] = l
	}
	l.refs++
	targetLocksMu.Unlock()

	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		targetLocksMu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(targetLocks, key)
		}
		targetLocksMu.Unlock()
	}
}

// Replace atomically commits a stream through one held parent.
func Replace(req ReplaceRequest) (Result, error) {
	return platformReplace(req)
}

// PathLocation scopes an effect to a file's immediate parent.
func PathLocation(abs string) Location {
	return Location{Root: filepath.Dir(abs), Rel: filepath.Base(abs)}
}

// stage names one step of Replace. Tests inject faults between stages.
type stage string

const (
	stageCreated    stage = "created"
	stageWritten    stage = "written"
	stageModeSet    stage = "mode-set"
	stageFileSynced stage = "file-synced"
	stageFileClosed stage = "file-closed"
	stageValidated  stage = "validated"
	stageRenamed    stage = "renamed"
	stageDirSynced  stage = "directory-synced"
	stageVerified   stage = "verified"
)

func injectAt(inject func(stage) error, at stage) error {
	if inject == nil {
		return nil
	}
	if err := inject(at); err != nil {
		return fmt.Errorf("injected failure after %s: %w", at, err)
	}
	return nil
}

func cleanLocation(loc Location) (Location, error) {
	root := loc.Root
	if root == "" || !filepath.IsAbs(root) {
		return Location{}, fmt.Errorf("%w: root must be absolute", ErrInvalidPath)
	}
	rel := filepath.Clean(loc.Rel)
	if rel == "" || rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return Location{}, fmt.Errorf("%w: %q", ErrInvalidPath, loc.Rel)
	}
	return Location{Root: filepath.Clean(root), Rel: rel}, nil
}

func cleanReadLocation(loc Location) (Location, error) {
	if loc.Rel == "" {
		return Location{}, fmt.Errorf("%w: %q", ErrInvalidPath, loc.Rel)
	}
	if loc.Rel == "." {
		root := loc.Root
		if root == "" || !filepath.IsAbs(root) {
			return Location{}, fmt.Errorf("%w: root must be absolute", ErrInvalidPath)
		}
		return Location{Root: filepath.Clean(root), Rel: "."}, nil
	}
	return cleanLocation(loc)
}
