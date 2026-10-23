package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
)

var errInvalidRestoreProgress = errors.New("restore progress is not usable")

const restoreProgressName = ".restore-progress.json"

type restoreProgress struct {
	Version   int    `json:"version"`
	Identity  string `json:"identity"`
	Completed int    `json:"completed"`
}

// Progress is an optional, versioned cursor bound to the immutable decision.
// Older binaries safely replay files if they do not understand this hint.
func (r *pendingRestore) checkpointProgress() error {
	if r.progressUnavailable {
		return nil
	}
	identity, err := r.progressBinding()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(restoreProgress{Version: 1, Identity: identity, Completed: r.completed})
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{Location: fseffect.PathLocation(filepath.Join(r.marker.StagingDir, restoreProgressName)), Source: bytes.NewReader(raw), Mode: fileMode})
	return err
}

func (r *pendingRestore) progressBinding() (string, error) {
	if r.progressIdentity != "" {
		return r.progressIdentity, nil
	}
	decision := r.marker
	decision.Applied = nil
	decision.FailedAt = ""
	decision.FailureDetail = ""
	raw, err := json.Marshal(decision)
	if err != nil {
		return "", err
	}
	r.progressIdentity = fmt.Sprintf("%x", sha256.Sum256(raw))
	return r.progressIdentity, nil
}

func (r *pendingRestore) loadProgress() error {
	err := r.readProgress()
	if errors.Is(err, errInvalidRestoreProgress) {
		// The decision and staged payloads remain authoritative. A hint cannot
		// prevent their idempotent replay or leave a partially trusted prefix.
		r.applied = nil
		r.appliedSet = make(map[string]struct{})
		r.completed = 0
		return nil
	}
	return err
}

func (r *pendingRestore) readProgress() error {
	path := filepath.Join(r.marker.StagingDir, restoreProgressName)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		// Do not replace an unexpected object just to persist an optional hint.
		// The immutable decision can still replay every payload on interruption.
		r.progressUnavailable = true
		return errInvalidRestoreProgress
	}
	if info.Size() > 4096 {
		return fmt.Errorf("invalid restore progress file: %w", errInvalidRestoreProgress)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var progress restoreProgress
	if err := decoder.Decode(&progress); err != nil {
		return fmt.Errorf("decode progress: %w", errInvalidRestoreProgress)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("ambiguous restore progress: %w", errInvalidRestoreProgress)
	}
	identity, err := r.progressBinding()
	if err != nil {
		return err
	}
	if progress.Version != 1 || progress.Identity != identity || progress.Completed < 0 || progress.Completed > len(r.marker.Files)+len(r.marker.DeleteRelPaths) {
		return fmt.Errorf("restore progress does not match its decision: %w", errInvalidRestoreProgress)
	}
	for index := range progress.Completed {
		var rel string
		if index < len(r.marker.Files) {
			entry := r.marker.Files[index]
			rel = entry.RelPath
			destination := filepath.Join(filepath.Dir(r.markerPath), filepath.FromSlash(r.marker.liveRelPath(rel)))
			sum, size, err := installedDigest(destination, entry.Kind)
			if os.IsNotExist(err) {
				return errInvalidRestoreProgress
			}
			if err != nil {
				return fmt.Errorf("verify checkpointed file: %w", err)
			}
			if sum != entry.SHA256 || size != entry.Size {
				return fmt.Errorf("checkpointed file does not match restore decision: %w", errInvalidRestoreProgress)
			}
		} else {
			rel = r.marker.DeleteRelPaths[index-len(r.marker.Files)]
			if _, err := os.Lstat(filepath.Join(filepath.Dir(r.markerPath), filepath.FromSlash(rel))); !os.IsNotExist(err) {
				if err != nil {
					return fmt.Errorf("inspect checkpointed deletion: %w", err)
				}
				return fmt.Errorf("checkpointed deletion is not complete: %w", errInvalidRestoreProgress)
			}
		}
		if _, found := r.appliedSet[rel]; !found {
			r.applied = append(r.applied, rel)
			r.appliedSet[rel] = struct{}{}
		}
	}
	r.completed = progress.Completed
	return nil
}
