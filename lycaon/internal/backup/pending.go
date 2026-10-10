package backup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fssync"
	"github.com/lycaon/lycaon/internal/localdata"
)

const fileMode = 0o600

type markerPublishedError struct{ cause error }

func (e *markerPublishedError) Error() string { return e.cause.Error() }
func (e *markerPublishedError) Unwrap() error { return e.cause }

type pendingReplacement struct {
	raw    []byte
	marker PendingMarker
}

// HasPendingRestore reports whether an unfailed staged restore transaction exists.
func HasPendingRestore(configDir string) bool {
	configDir = strings.TrimSpace(configDir)
	if configDir == "" {
		return false
	}
	markerPath := PendingMarkerPath(configDir)
	raw, err := os.ReadFile(markerPath)
	if err != nil {
		return false
	}
	var marker PendingMarker
	if err := json.Unmarshal(raw, &marker); err != nil || strings.TrimSpace(marker.FailedAt) != "" {
		return false
	}
	_, err = loadPendingRestore(configDir)
	return err == nil
}

// A failed transaction remains intact until its replacement is fully staged.
func readReplaceablePending(markerPath string) (*pendingReplacement, error) {
	raw, err := os.ReadFile(markerPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("backup: inspect pending marker: %w", err)
	}
	previous := &pendingReplacement{raw: raw}
	if err := json.Unmarshal(raw, &previous.marker); err == nil && strings.TrimSpace(previous.marker.FailedAt) == "" {
		if _, loadErr := loadPendingRestore(filepath.Dir(markerPath)); loadErr == nil {
			return nil, ErrPending
		}
	}
	return previous, nil
}

func (p *pendingReplacement) recoveryDir(configDir string) string {
	if p == nil {
		return ""
	}
	path, _, err := validateRestoreTransactionDir(configDir, p.marker.RecoveryDir, localdata.RestorePreImageDirPrefix)
	if err != nil {
		return ""
	}
	return path
}

func publishPendingMarker(path string, marker PendingMarker, previous *pendingReplacement) error {
	if previous == nil {
		return writeMarkerExclusive(path, marker)
	}
	raw, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	validated := false
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(path), Source: bytes.NewReader(raw), Mode: fileMode,
		BeforeCommit: func(current fseffect.Target, _ fseffect.Result) error {
			file, err := current.Open()
			if err != nil {
				return err
			}
			defer func() { _ = file.Close() }()
			got, err := io.ReadAll(io.LimitReader(file, int64(len(previous.raw))+1))
			if err != nil {
				return err
			}
			if !bytes.Equal(got, previous.raw) {
				return ErrPending
			}
			validated = true
			return nil
		},
	})
	if err != nil {
		if validated {
			// A sync or readback failure leaves publication uncertain; both payloads remain.
			return &markerPublishedError{cause: err}
		}
		return err
	}
	if staging, _, err := validateRestoreTransactionDir(filepath.Dir(path), previous.marker.StagingDir, localdata.RestoreStagingDirPrefix); err == nil {
		if err := os.RemoveAll(staging); err != nil {
			slog.Warn("could not remove superseded restore staging", "path", staging, "error", err)
		}
	}
	return nil
}

func (r *pendingRestore) recordApplied(rel string) error {
	r.applied = append(r.applied, rel)
	r.appliedSet[rel] = struct{}{}
	r.marker.Applied = r.applied
	return writeMarker(r.markerPath, r.marker)
}

// The failure marker allows an explicit recovery action to replace the transaction.
func (r *pendingRestore) recordFailure(cause error) {
	r.marker.Applied = r.applied
	r.marker.FailedAt = time.Now().UTC().Format(time.RFC3339)
	r.marker.FailureDetail = cause.Error()
	_ = writeMarker(r.markerPath, r.marker)
}

func writeMarker(path string, marker PendingMarker) error {
	raw, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(path),
		Source:   bytes.NewReader(raw),
		Mode:     fileMode,
		DirMode:  0o700,
	})
	return err
}

func writeMarkerExclusive(path string, marker PendingMarker) error {
	raw, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".restore-marker-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(fileMode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := fssync.File(tmp); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmpPath, path); err != nil {
		return err
	}
	if err := os.Remove(tmpPath); err != nil {
		return &markerPublishedError{cause: err}
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return &markerPublishedError{cause: err}
	}
	return nil
}
