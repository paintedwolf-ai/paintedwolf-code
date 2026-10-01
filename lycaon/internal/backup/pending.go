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

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/localdata"
)

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
