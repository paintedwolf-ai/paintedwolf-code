// Package repochange broadcasts repository mutations to cache observers.
package repochange

import (
	"context"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// Kind selects which cache projections become stale.
type Kind int

const (
	// HeadMoved means a commit landed — committed history changed.
	HeadMoved Kind = iota + 1
	// WorktreeChanged means files changed in the working tree.
	WorktreeChanged
	// IndexChanged invalidates status without asserting a source or HEAD change.
	IndexChanged
)

type Event struct {
	// ProjectDir is the repo root (absolute) whose state changed.
	ProjectDir string
	Kind       Kind
	// Paths are the changed paths for WorktreeChanged; empty for HeadMoved.
	Paths []string
	// Changes classifies WorktreeChanged paths from structured producer facts.
	Changes []WorktreeChange
	// Source is who produced the signal (mutation|watcher|git_host|ttl).
	Source Source
	// WorktreeEpoch is the root generation after this event was accepted.
	WorktreeEpoch uint64
	// EpochBootID distinguishes generations created by different host processes.
	EpochBootID string
}

// WorktreeChangeKind distinguishes content edits from membership changes.
type WorktreeChangeKind int

const (
	WorktreeChangeUnknown WorktreeChangeKind = iota
	WorktreeChangeContent
	WorktreeChangeStructural
	WorktreeChangeResync
)

type WorktreeChange struct {
	Path string
	Kind WorktreeChangeKind
}

type StructuralPathSet struct {
	Paths []string
	Full  bool
}

func StructuralPaths(ev Event) StructuralPathSet {
	if ev.Kind != WorktreeChanged {
		return StructuralPathSet{}
	}
	changes := normalizeWorktreeChanges(ev.Paths, ev.Changes)
	if len(changes) == 0 {
		return StructuralPathSet{Full: true}
	}
	paths := make([]string, 0, len(changes))
	for _, change := range changes {
		switch change.Kind {
		case WorktreeChangeContent:
			continue
		case WorktreeChangeResync:
			return StructuralPathSet{Full: true}
		default:
			paths = append(paths, change.Path)
		}
	}
	return StructuralPathSet{Paths: paths}
}

// Epoch is one process-monotonic worktree generation.
type Epoch struct {
	BootID string
	Value  uint64
}

// Observer runs synchronously on the producer's path after a change lands.
type Observer func(ctx context.Context, ev Event)

var (
	mu          sync.RWMutex
	observers   map[uint64]Observer
	observerSeq uint64
	epochMu     sync.Mutex
	epochBoot   = uuid.NewString()
	epochByDir  = map[string]uint64{}
)

func RegisterObserver(obs Observer) func() {
	if obs == nil {
		return func() {}
	}
	mu.Lock()
	if observers == nil {
		observers = make(map[uint64]Observer)
	}
	observerSeq++
	id := observerSeq
	observers[id] = obs
	mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			mu.Lock()
			delete(observers, id)
			mu.Unlock()
		})
	}
}

func Notify(ctx context.Context, ev Event) {
	if ev.ProjectDir == "" || ev.Kind == 0 {
		return
	}
	dir := canonicalDir(ev.ProjectDir)
	if dir == "" {
		return
	}
	ev.ProjectDir = dir
	if ev.Kind == WorktreeChanged {
		ev.Changes = normalizeWorktreeChanges(ev.Paths, ev.Changes)
		ev.Paths = pathsFromChanges(ev.Changes)
	}
	if ev.Kind == WorktreeChanged && ev.Source == SourceWatcher && len(ev.Changes) > 0 {
		changes := ev.Changes[:0]
		for _, change := range ev.Changes {
			if !IsPrivatePath(filepath.Join(dir, change.Path)) {
				changes = append(changes, change)
			}
		}
		if len(changes) == 0 {
			return
		}
		ev.Changes = changes
		ev.Paths = pathsFromChanges(changes)
	}
	if ev.WorktreeEpoch == 0 || ev.EpochBootID == "" {
		epoch := Advance(dir)
		ev.WorktreeEpoch = epoch.Value
		ev.EpochBootID = epoch.BootID
	}
	mu.RLock()
	snapshot := make([]Observer, 0, len(observers))
	for _, observer := range observers {
		snapshot = append(snapshot, observer)
	}
	mu.RUnlock()
	for _, fn := range snapshot {
		fn(ctx, ev)
	}
}

func normalizeWorktreeChanges(paths []string, changes []WorktreeChange) []WorktreeChange {
	byPath := make(map[string]WorktreeChangeKind, len(paths)+len(changes))
	order := make([]string, 0, len(paths)+len(changes))
	remember := func(path string, kind WorktreeChangeKind) {
		path = filepath.ToSlash(path)
		if path == "" {
			return
		}
		if previous, found := byPath[path]; found {
			byPath[path] = mergeWorktreeChangeKind(previous, kind)
			return
		}
		order = append(order, path)
		byPath[path] = kind
	}
	for _, change := range changes {
		remember(change.Path, change.Kind)
	}
	for _, path := range paths {
		if _, found := byPath[filepath.ToSlash(path)]; !found {
			remember(path, WorktreeChangeUnknown)
		}
	}
	result := make([]WorktreeChange, 0, len(order))
	for _, path := range order {
		result = append(result, WorktreeChange{Path: path, Kind: byPath[path]})
	}
	return result
}

func pathsFromChanges(changes []WorktreeChange) []string {
	paths := make([]string, 0, len(changes))
	for _, change := range changes {
		paths = append(paths, change.Path)
	}
	return paths
}

func mergeWorktreeChangeKind(left, right WorktreeChangeKind) WorktreeChangeKind {
	if left == WorktreeChangeResync || right == WorktreeChangeResync {
		return WorktreeChangeResync
	}
	if left == WorktreeChangeUnknown || right == WorktreeChangeUnknown {
		return WorktreeChangeUnknown
	}
	if left == WorktreeChangeStructural || right == WorktreeChangeStructural {
		return WorktreeChangeStructural
	}
	if left == WorktreeChangeContent || right == WorktreeChangeContent {
		return WorktreeChangeContent
	}
	return WorktreeChangeUnknown
}

// Advance marks root dirty immediately and returns its new generation.
func Advance(root string) Epoch {
	root = canonicalDir(root)
	epochMu.Lock()
	defer epochMu.Unlock()
	epochByDir[root]++
	return Epoch{BootID: epochBoot, Value: epochByDir[root]}
}

// CurrentEpoch returns the current generation, initializing a clean root at one.
func CurrentEpoch(root string) Epoch {
	root = canonicalDir(root)
	epochMu.Lock()
	defer epochMu.Unlock()
	if epochByDir[root] == 0 {
		epochByDir[root] = 1
	}
	return Epoch{BootID: epochBoot, Value: epochByDir[root]}
}

// EpochCurrent reports whether root still has the supplied generation.
func EpochCurrent(root string, epoch Epoch) bool { return CurrentEpoch(root) == epoch }

func canonicalDir(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return ""
	}
	return filepath.Clean(abs)
}

// ResetObserversForTest clears observers (unit tests only).
func ResetObserversForTest() {
	mu.Lock()
	observers = nil
	observerSeq = 0
	mu.Unlock()
	epochMu.Lock()
	epochBoot = uuid.NewString()
	clear(epochByDir)
	epochMu.Unlock()
}
