// Package workerplacement decides where a worker branch lives and keeps the
// state-folder registry that lets every lifecycle find it again.
//
// worker_jobs keeps the logical worker-branches/<key>/<job> address, so a store,
// backup or restore never holds a volume path. The registry maps that address to
// a folder on another volume; a branch with no record lives in the state folder.
package workerplacement

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/filelock"
	"github.com/lycaon/lycaon/internal/fseffect"
)

const registryFormat = 1

// ErrUnknownRegistryFormat refuses a registry this build cannot interpret. The
// file is left unchanged, as it names work on volumes that may be absent.
var ErrUnknownRegistryFormat = errors.New("unknown worker placement registry format")

// Kind says why a folder exists where it does.
type Kind string

const (
	// KindVolumeRoot is a folder at the root of a source's volume.
	KindVolumeRoot Kind = "volume_root"
	// KindOutsideRoots is the last location for a copy when the state folder is
	// inside an attached root. It makes no claim about the source's volume.
	KindOutsideRoots Kind = "outside_roots"
)

// Folder is one registered engine-managed placement folder.
type Folder struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	VolumeRoot string `json:"volume_root"`
	Kind       Kind   `json:"kind"`
}

// BranchesRoot is the branch base inside the folder. It keeps the
// worker-branches path segment that guards and rewriting recognize.
func (f Folder) BranchesRoot() string {
	return enginepaths.WorkerBranchesRootUnder(f.Path)
}

// Registry is the decoded state-folder record.
type Registry struct {
	FormatVersion int               `json:"format_version"`
	Folders       []Folder          `json:"folders"`
	Branches      map[string]string `json:"branches"`
}

func registryPath(stateRoot string) string {
	return filepath.Join(strings.TrimSpace(stateRoot), enginepaths.WorkerPlacementsFileName)
}

func lockPath(stateRoot string) string {
	return filepath.Join(strings.TrimSpace(stateRoot), "locks", "worker-placements.lock")
}

// BranchKey is the registry key of one job's branch.
func BranchKey(projectKey, jobID string) string {
	return projectKey + "/" + jobID
}

func (r Registry) folder(id string) (Folder, bool) {
	for _, folder := range r.Folders {
		if folder.ID == id {
			return folder, true
		}
	}
	return Folder{}, false
}

func decodeRegistry(raw []byte) (Registry, error) {
	var head struct {
		FormatVersion int `json:"format_version"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return Registry{}, fmt.Errorf("%w: %w", ErrUnknownRegistryFormat, err)
	}
	if head.FormatVersion != registryFormat {
		return Registry{}, fmt.Errorf("%w: %d", ErrUnknownRegistryFormat, head.FormatVersion)
	}
	var out Registry
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&out); err != nil {
		return Registry{}, fmt.Errorf("%w: %w", ErrUnknownRegistryFormat, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Registry{}, fmt.Errorf("%w: trailing data", ErrUnknownRegistryFormat)
	}
	if out.Branches == nil {
		out.Branches = map[string]string{}
	}
	return out, nil
}

type cachedRegistry struct {
	info     os.FileInfo
	registry Registry
}

var (
	cacheMu sync.Mutex
	cache   = map[string]cachedRegistry{}
)

// Load reads the registry. Branch reads consult it per row, so an unchanged file
// is not decoded again; every write publishes a new inode.
func Load(stateRoot string) (Registry, error) {
	path := registryPath(stateRoot)
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Registry{FormatVersion: registryFormat, Branches: map[string]string{}}, nil
	}
	if err != nil {
		return Registry{}, err
	}
	cacheMu.Lock()
	entry, ok := cache[path]
	cacheMu.Unlock()
	if ok && os.SameFile(entry.info, info) && entry.info.ModTime().Equal(info.ModTime()) && entry.info.Size() == info.Size() {
		return entry.registry, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Registry{}, err
	}
	registry, err := decodeRegistry(raw)
	if err != nil {
		return Registry{}, err
	}
	cacheMu.Lock()
	cache[path] = cachedRegistry{info: info, registry: registry}
	cacheMu.Unlock()
	return registry, nil
}

var mutateMu sync.Mutex

// mutate applies fn under the in-process and cross-process locks.
func mutate(ctx context.Context, stateRoot string, fn func(*Registry) error) error {
	mutateMu.Lock()
	defer mutateMu.Unlock()
	lock, err := filelock.Open(lockPath(stateRoot))
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		held, lockErr := filelock.TryExclusive(lock)
		if lockErr != nil {
			return lockErr
		}
		if held {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	defer func() { _ = filelock.Unlock(lock) }()
	registry, err := Load(stateRoot)
	if err != nil {
		return err
	}
	next := Registry{FormatVersion: registryFormat, Folders: append([]Folder(nil), registry.Folders...), Branches: make(map[string]string, len(registry.Branches))}
	for key, id := range registry.Branches {
		next.Branches[key] = id
	}
	if err := fn(&next); err != nil {
		return err
	}
	return save(stateRoot, next)
}

func save(stateRoot string, registry Registry) error {
	sort.Slice(registry.Folders, func(i, j int) bool { return registry.Folders[i].ID < registry.Folders[j].ID })
	defer Publish(registry)
	if len(registry.Folders) == 0 && len(registry.Branches) == 0 {
		if err := os.Remove(registryPath(stateRoot)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	raw, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(registryPath(stateRoot)),
		Source:   bytes.NewReader(append(raw, '\n')),
		Mode:     0o600,
		DirMode:  0o700,
	})
	return err
}

// Publish hands the registered folders to path rewriting.
func Publish(registry Registry) {
	folders := make([]string, 0, len(registry.Folders))
	for _, folder := range registry.Folders {
		folders = append(folders, folder.Path)
	}
	enginepaths.PublishWorkerPlacementFolders(folders)
}

// Reachable reports whether a folder is reachable right now. The marker is on the
// folder's own volume, so a bare mount point left by an unmount does not count.
func Reachable(folder Folder) bool {
	marker, err := readMarker(folder.Path)
	return err == nil && marker.ID == folder.ID
}

// AvailableFolders lists the registered folders reachable right now.
func AvailableFolders(stateRoot string) ([]Folder, error) {
	registry, err := Load(stateRoot)
	if err != nil {
		return nil, err
	}
	var out []Folder
	for _, folder := range registry.Folders {
		if Reachable(folder) {
			out = append(out, folder)
		}
	}
	return out, nil
}

// PhysicalBranchRoot maps a job's logical address to where its tree lives. A tree
// in the state folder wins over a record: restore put it there on purpose.
func PhysicalBranchRoot(stateRoot, projectKey, jobID string) (root string, placed bool, err error) {
	stateTree := filepath.Join(enginepaths.WorkerBranchesRootUnder(stateRoot), projectKey, jobID)
	registry, err := Load(stateRoot)
	if err != nil {
		return "", false, err
	}
	id, ok := registry.Branches[BranchKey(projectKey, jobID)]
	if !ok {
		return stateTree, false, nil
	}
	folder, ok := registry.folder(id)
	if !ok {
		return stateTree, false, nil
	}
	if _, err := os.Lstat(stateTree); err == nil {
		return stateTree, false, nil
	}
	return filepath.Join(folder.BranchesRoot(), projectKey, jobID), true, nil
}

// PhysicalFromLogical resolves a stored worker-branches/<key>/<job> address.
func PhysicalFromLogical(stateRoot, rel string) (string, error) {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) != 3 || parts[0] != enginepaths.WorkerBranchesDirName {
		return filepath.Join(stateRoot, filepath.FromSlash(rel)), nil
	}
	root, _, err := PhysicalBranchRoot(stateRoot, parts[1], parts[2])
	return root, err
}

// LogicalFromPhysical returns the stored address of a tree, in the state folder
// or under a registered placement folder.
func LogicalFromPhysical(stateRoot, root string) (string, bool, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if !filepath.IsAbs(root) {
		return "", false, nil
	}
	if logical, ok := logicalUnder(stateRoot, root); ok {
		return logical, true, nil
	}
	registry, err := Load(stateRoot)
	if err != nil {
		return "", false, err
	}
	for _, folder := range registry.Folders {
		if logical, ok := logicalUnder(folder.Path, root); ok {
			return logical, true, nil
		}
	}
	return "", false, nil
}

func logicalUnder(base, root string) (string, bool) {
	rel, err := filepath.Rel(base, root)
	if err != nil || !filepath.IsLocal(rel) {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if !strings.HasPrefix(rel, enginepaths.WorkerBranchesDirName+"/") {
		return "", false
	}
	return rel, true
}

// PlacedBranch is one registry record with its current availability.
type PlacedBranch struct {
	ProjectKey string
	JobID      string
	Root       string
	Folder     Folder
	Available  bool
}

// PlacedBranches lists every recorded branch, reachable or not.
func PlacedBranches(stateRoot string) ([]PlacedBranch, error) {
	registry, err := Load(stateRoot)
	if err != nil {
		return nil, err
	}
	available := make(map[string]bool, len(registry.Folders))
	out := make([]PlacedBranch, 0, len(registry.Branches))
	for key, id := range registry.Branches {
		folder, ok := registry.folder(id)
		projectKey, jobID, split := strings.Cut(key, "/")
		if !ok || !split {
			continue
		}
		reachable, seen := available[id]
		if !seen {
			reachable = Reachable(folder)
			available[id] = reachable
		}
		out = append(out, PlacedBranch{
			ProjectKey: projectKey, JobID: jobID, Folder: folder, Available: reachable,
			Root: filepath.Join(folder.BranchesRoot(), projectKey, jobID),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Root < out[j].Root })
	return out, nil
}
