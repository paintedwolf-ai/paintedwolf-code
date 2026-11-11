package workerplacement

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fseffect"
)

const markerFileName = "placement.json"

// marker lives in the folder, so it travels with a volume that moves.
type marker struct {
	FormatVersion int    `json:"format_version"`
	ID            string `json:"id"`
	StateKey      string `json:"state_key"`
}

func readMarker(folder string) (marker, error) {
	raw, err := readPrivateFile(folder, markerFileName)
	if err != nil {
		return marker{}, err
	}
	var out marker
	if err := json.Unmarshal(raw, &out); err != nil {
		return marker{}, err
	}
	if out.FormatVersion != registryFormat {
		return marker{}, fmt.Errorf("%w: marker %d", ErrUnknownRegistryFormat, out.FormatVersion)
	}
	return out, nil
}

func writeMarker(folder, id, stateRoot string) error {
	raw, err := json.Marshal(marker{FormatVersion: registryFormat, ID: id, StateKey: enginepaths.ProjectKey(stateRoot)})
	if err != nil {
		return err
	}
	return writeFolderFile(filepath.Join(folder, markerFileName), raw)
}

func writeFolderFile(path string, raw []byte) error {
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(path),
		Source:   bytes.NewReader(raw),
		Mode:     0o600,
		DirMode:  0o700,
	})
	return err
}

// reserve registers the folder before its first byte so a crash cannot leave one
// the registry does not name. It creates one directory and nothing above it, so
// a read-only or foreign volume root simply fails. The name is predictable and the
// parent may be shared, so a folder is trusted only once it proves private to this
// user; anything else is refused with nothing written. A folder found here with
// this installation's marker was the same one before its volume moved mount points.
func reserve(ctx context.Context, stateRoot, path, volumeRoot string, kind Kind) (folder Folder, created bool, err error) {
	if volumeIgnoresOwnership(filepath.Dir(path)) {
		return Folder{}, false, ErrVolumeIgnoresOwnership
	}
	existing, readErr := readMarker(path)
	if errors.Is(readErr, ErrFolderNotPrivate) {
		return Folder{}, false, readErr
	}
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		return Folder{}, false, fmt.Errorf("placement marker is unreadable: %w: %w", fs.ErrPermission, readErr)
	}
	if readErr == nil {
		if existing.StateKey != enginepaths.ProjectKey(stateRoot) {
			return Folder{}, false, fmt.Errorf("placement folder belongs to another installation: %w", fs.ErrPermission)
		}
		folder = Folder{ID: existing.ID, Path: path, VolumeRoot: volumeRoot, Kind: kind}
		return folder, false, mutate(ctx, stateRoot, func(registry *Registry) error {
			for i := range registry.Folders {
				if registry.Folders[i].ID == folder.ID {
					registry.Folders[i] = folder
					return nil
				}
			}
			registry.Folders = append(registry.Folders, folder)
			return nil
		})
	}
	folder = Folder{ID: uuid.NewString(), Path: path, VolumeRoot: volumeRoot, Kind: kind}
	if err := mutate(ctx, stateRoot, func(registry *Registry) error {
		registry.Folders = append(registry.Folders, folder)
		return nil
	}); err != nil {
		return Folder{}, false, err
	}
	undo := func(cause error) (Folder, bool, error) {
		return Folder{}, false, errors.Join(cause, dropFolder(context.WithoutCancel(ctx), stateRoot, folder.ID))
	}
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return undo(err)
	}
	// Someone else may have made it first, or swapped it after this check; reading
	// the marker back proves the private folder holds what this call wrote.
	if err := verifyPrivateFolder(path); err != nil {
		return undo(err)
	}
	if err := writeMarker(path, folder.ID, stateRoot); err != nil {
		return undo(err)
	}
	if written, err := readMarker(path); err != nil || written.ID != folder.ID {
		return undo(errors.Join(ErrFolderNotPrivate, err))
	}
	if err := excludeFromBackupsAndIndexing(path); err != nil {
		slog.WarnContext(ctx, "exclude worker placement folder from backups and indexing", "path", path, "err", err)
	}
	return folder, true, nil
}

func dropFolder(ctx context.Context, stateRoot, id string) error {
	return mutate(ctx, stateRoot, func(registry *Registry) error {
		kept := registry.Folders[:0]
		for _, folder := range registry.Folders {
			if folder.ID != id {
				kept = append(kept, folder)
			}
		}
		registry.Folders = kept
		for key, owner := range registry.Branches {
			if owner == id {
				delete(registry.Branches, key)
			}
		}
		return nil
	})
}

// release removes a folder this decision created and no branch uses.
func release(ctx context.Context, stateRoot string, folder Folder) error {
	return errors.Join(removeMarkedFolder(folder), dropFolder(ctx, stateRoot, folder.ID))
}

// removeMarkedFolder deletes only a folder that proves it is the registered one.
func removeMarkedFolder(folder Folder) error {
	if !enginepaths.IsWorkerPlacementDirName(filepath.Base(folder.Path)) {
		return fmt.Errorf("refusing to remove unrecognized placement folder")
	}
	if !Reachable(folder) {
		return fmt.Errorf("refusing to remove unavailable placement folder")
	}
	return os.RemoveAll(folder.Path)
}

// FolderPresent reports whether path still holds a placement marker. It answers
// for code that has only a branch path, before anything creates directories there.
func FolderPresent(path string) bool {
	_, err := readMarker(path)
	return err == nil
}

// RecordBranch binds a job's logical address to a folder.
func RecordBranch(ctx context.Context, stateRoot, folderID, projectKey, jobID string) error {
	return mutate(ctx, stateRoot, func(registry *Registry) error {
		if _, ok := registry.folder(folderID); !ok {
			return fmt.Errorf("worker placement folder %s is not registered", folderID)
		}
		registry.Branches[BranchKey(projectKey, jobID)] = folderID
		return nil
	})
}

// ForgetBranch drops a job's record and removes a folder nothing else uses. A
// folder on an absent volume keeps its record until the volume returns.
func ForgetBranch(ctx context.Context, stateRoot, projectKey, jobID string) error {
	var emptied *Folder
	err := mutate(ctx, stateRoot, func(registry *Registry) error {
		key := BranchKey(projectKey, jobID)
		id, ok := registry.Branches[key]
		if !ok {
			return nil
		}
		delete(registry.Branches, key)
		for _, other := range registry.Branches {
			if other == id {
				return nil
			}
		}
		if folder, found := registry.folder(id); found && Reachable(folder) {
			emptied = &folder
		}
		return nil
	})
	if err != nil || emptied == nil {
		return err
	}
	return release(ctx, stateRoot, *emptied)
}

// PurgeReport says what a purge removed and what it could not reach.
type PurgeReport struct {
	Removed     []string
	Unavailable []string
}

// Purge removes every reachable folder and its records. A folder on an absent
// volume stays recorded, so a later purge can still find it.
func Purge(ctx context.Context, stateRoot string) (PurgeReport, error) {
	registry, err := Load(stateRoot)
	if err != nil {
		return PurgeReport{}, err
	}
	var report PurgeReport
	var errs []error
	for _, folder := range registry.Folders {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if !Reachable(folder) {
			report.Unavailable = append(report.Unavailable, folder.Path)
			continue
		}
		if err := release(ctx, stateRoot, folder); err != nil {
			errs = append(errs, err)
			continue
		}
		report.Removed = append(report.Removed, folder.Path)
	}
	return report, errors.Join(errs...)
}
