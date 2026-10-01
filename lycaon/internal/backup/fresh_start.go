package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/localdata"
)

// FreshStartOpts configures a clean-store transaction.
type FreshStartOpts struct {
	ConfigDir  string
	DBPath     string
	SQLDB      db.DBTX
	AppVersion string
	Now        time.Time
}

// StageFreshStart publishes a durable fresh-store transaction for next launch.
func StageFreshStart(ctx context.Context, opts FreshStartOpts) (StageResult, error) {
	stageMu.Lock()
	defer stageMu.Unlock()
	if opts.ConfigDir == "" {
		return StageResult{}, fmt.Errorf("backup: config dir required")
	}
	liveName, err := liveStoreFilename(opts.ConfigDir, opts.DBPath)
	if err != nil {
		return StageResult{}, err
	}
	markerPath := PendingMarkerPath(opts.ConfigDir)
	previous, err := readReplaceablePending(markerPath)
	if err != nil {
		return StageResult{}, err
	}

	now := opts.Now.UTC()
	if opts.Now.IsZero() {
		now = time.Now().UTC()
	}
	transactionID := uuid.NewString()
	stagingDir := filepath.Join(opts.ConfigDir, localdata.RestoreStagingDirPrefix+"-"+transactionID)
	recoveryDir := filepath.Join(opts.ConfigDir, localdata.RestorePreImageDirPrefix+"-"+transactionID)
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return StageResult{}, fmt.Errorf("backup: create fresh-start staging: %w", err)
	}
	cleanup := func() {
		_ = os.RemoveAll(stagingDir)
		_ = os.RemoveAll(recoveryDir)
	}

	stagedStore := filepath.Join(stagingDir, storeRelPath)
	if err := createFreshStore(ctx, stagedStore); err != nil {
		cleanup()
		return StageResult{}, err
	}
	sum, size, err := sha256File(stagedStore)
	if err != nil {
		cleanup()
		return StageResult{}, fmt.Errorf("backup: hash fresh store: %w", err)
	}
	if err := validateStagedStore(ctx, stagedStore, db.SchemaVersion); err != nil {
		cleanup()
		return StageResult{}, err
	}

	stageOpts := StageOpts{ConfigDir: opts.ConfigDir, DBPath: opts.DBPath, SQLDB: opts.SQLDB}
	if err := writeRecoveryCopy(ctx, stageOpts, recoveryDir,
		localdata.StoreResetRecoveryRelPaths(), localdata.StoreResetRecoveryRelDirs()); err != nil {
		cleanup()
		return StageResult{}, err
	}
	marker := PendingMarker{
		LiveStoreFilename: liveName,
		Operation:         PendingOperationFreshStart,
		StagingDir:        stagingDir,
		RecoveryDir:       recoveryDir,
		SourceAppVersion:  opts.AppVersion,
		CreatedAt:         now.Format(time.RFC3339),
		Files: []PendingFile{{
			RelPath: storeRelPath,
			Kind:    fileKindRegular,
			Mode:    fileMode,
			SHA256:  sum,
			Size:    size,
		}},
		DeleteRelPaths: []string{localdata.FirstRunOnboardingRelPath()},
	}
	if err := publishPendingMarker(markerPath, marker, previous); err != nil {
		var published *markerPublishedError
		if !errors.As(err, &published) {
			cleanup()
		}
		if os.IsExist(err) {
			return StageResult{}, ErrPending
		}
		return StageResult{}, fmt.Errorf("backup: publish fresh-start marker: %w", err)
	}
	return StageResult{
		RestartRequired:       true,
		RecoveryCopyPath:      recoveryDir,
		ReclaimedPreImages:    reclaimSupersededPreImages(opts.ConfigDir, recoveryDir, previous.recoveryDir(opts.ConfigDir)),
		SupersededTransaction: previous.recoveryDir(opts.ConfigDir),
	}, nil
}

func createFreshStore(ctx context.Context, path string) error {
	store, err := db.Open(path) //nolint:contextcheck // db.Open bounds schema initialization internally and accepts no context.
	if err != nil {
		return fmt.Errorf("backup: create fresh store: %w", err)
	}
	if err := store.Shutdown(ctx); err != nil {
		return fmt.Errorf("backup: close fresh store: %w", err)
	}
	return nil
}
