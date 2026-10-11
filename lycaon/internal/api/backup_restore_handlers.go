package api

import (
	"errors"
	"io"
	"net/http"
	"os"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/fssync"
	"github.com/lycaon/lycaon/internal/version"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Storage) handleRestoreBackup(w http.ResponseWriter, r *http.Request) {
	if err := httpio.RequireRequestMediaType(r, "application/zip", "application/x-zip-compressed"); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if r.ContentLength > backup.MaxArchiveBytes {
		s.responses.Fail(w, wire.ApiErrorCodeBodyTooLarge, "compressed archive exceeds size limit")
		return
	}
	target, err := s.restoreTarget()
	if err != nil {
		s.responses.Fail(w, wire.ApiErrorCodeBackupUnavailable, "backup restore target is unavailable")
		return
	}
	upload, err := os.CreateTemp(s.dataDir, ".restore-upload-*.zip")
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	uploadPath := upload.Name()
	defer func() { _ = os.Remove(uploadPath) }()
	written, copyErr := io.Copy(upload, io.LimitReader(r.Body, backup.MaxArchiveBytes+1))
	if copyErr == nil {
		copyErr = fssync.File(upload)
	}
	if closeErr := upload.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		s.responses.Fail(w, wire.ApiErrorCodeBackupInvalid, "failed to store archive body")
		return
	}
	if written > backup.MaxArchiveBytes {
		s.responses.Fail(w, wire.ApiErrorCodeBodyTooLarge, "compressed archive exceeds size limit")
		return
	}
	result, err := backup.Stage(r.Context(), backup.StageOpts{
		DBPath:        target.path,
		ConfigDir:     s.dataDir,
		ArchivePath:   uploadPath,
		SQLDB:         target.snapshot,
		SchemaVersion: db.SchemaVersion,
	})
	if err != nil {
		switch {
		case errors.Is(err, backup.ErrPending):
			s.markRestorePending()
			s.responses.Fail(w, wire.ApiErrorCodeBackupRestorePending, "a restore is already staged; restart to apply")
		case errors.Is(err, backup.ErrBaselineMismatch):
			s.responses.Fail(w, wire.ApiErrorCodeBackupIncompatible, "backup is incompatible with current schema or platform")
		case errors.Is(err, backup.ErrInvalid):
			s.responses.Fail(w, wire.ApiErrorCodeBackupInvalid, "backup archive is corrupted or invalid")
		default:
			s.responses.InternalError(w, r, err)
		}
		return
	}
	s.markRestorePending()
	httpio.WriteJSON(w, http.StatusOK, wire.BackupRestoreResult{
		RestartRequired:  result.RestartRequired,
		RecoveryCopyPath: result.RecoveryCopyPath,
	})
}

func (s *Storage) handleRestoreRecoverySnapshot(w http.ResponseWriter, r *http.Request) {
	target, err := s.restoreTarget()
	if err != nil {
		s.responses.Fail(w, wire.ApiErrorCodeBackupUnavailable, "backup restore target is unavailable")
		return
	}

	result, err := backup.StageLatestUpgradeRecovery(r.Context(), backup.StageOpts{
		DBPath:        target.path,
		ConfigDir:     s.dataDir,
		SQLDB:         target.snapshot,
		SchemaVersion: db.SchemaVersion,
	})
	if err != nil {
		switch {
		case errors.Is(err, backup.ErrPending):
			s.markRestorePending()
			s.responses.Fail(w, wire.ApiErrorCodeBackupRestorePending, "a restore is already staged; restart to apply")
		case os.IsNotExist(err):
			s.responses.Fail(w, wire.ApiErrorCodeBackupIncompatible, "no compatible recovery snapshot is available")
		case errors.Is(err, backup.ErrBaselineMismatch):
			s.responses.Fail(w, wire.ApiErrorCodeBackupIncompatible, "recovery snapshot is incompatible with current schema or platform")
		case errors.Is(err, backup.ErrInvalid):
			s.responses.Fail(w, wire.ApiErrorCodeRecoverySnapshotInvalid, "recovery snapshot is invalid")
		default:
			s.responses.InternalError(w, r, err)
		}
		return
	}
	s.markRestorePending()
	httpio.WriteJSON(w, http.StatusOK, wire.BackupRestoreResult{
		RestartRequired:  result.RestartRequired,
		RecoveryCopyPath: result.RecoveryCopyPath,
	})
}

func (s *Storage) handleResetStore(w http.ResponseWriter, r *http.Request) {
	target, err := s.restoreTarget()
	if err != nil {
		s.responses.Fail(w, wire.ApiErrorCodeBackupUnavailable, "backup restore target is unavailable")
		return
	}
	result, err := backup.StageFreshStart(r.Context(), backup.FreshStartOpts{
		ConfigDir:  s.dataDir,
		DBPath:     target.path,
		SQLDB:      target.snapshot,
		AppVersion: version.Version,
	})
	if err != nil {
		if errors.Is(err, backup.ErrPending) {
			s.markRestorePending()
			s.responses.Fail(w, wire.ApiErrorCodeBackupRestorePending, "a restore is already staged; restart to apply")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	s.markRestorePending()
	httpio.WriteJSON(w, http.StatusOK, wire.BackupRestoreResult{
		RestartRequired:  result.RestartRequired,
		RecoveryCopyPath: result.RecoveryCopyPath,
	})
}
