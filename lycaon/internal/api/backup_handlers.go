package api

import (
	"errors"
	"math"
	"net/http"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/version"
	wire "github.com/lycaon/lycaon/pkg/api"
)

var errDurableStoreUnavailable = errors.New("durable store is unavailable")

func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	sqlDB, dbPath, err := s.liveBackupStore()
	if err != nil {
		s.responses.Fail(w, wire.ApiErrorCodeBackupUnavailable, "backup needs the durable store on disk")
		return
	}
	schemaVer, err := db.ReadUserVersion(r.Context(), sqlDB)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	archivePath, err := unusedTempPath(s.dataDir, ".backup-export-*.zip")
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	defer func() { _ = os.Remove(archivePath) }()
	manifest, err := backup.Create(r.Context(), backup.CreateOpts{
		ConfigDir:         s.dataDir,
		SQLDB:             sqlDB,
		DBPath:            dbPath,
		AppVersion:        version.Version,
		SchemaUserVersion: schemaVer,
	}, archivePath)
	if err != nil {
		if errors.Is(err, backup.ErrCaptureIncomplete) {
			s.responses.Fail(w, wire.ApiErrorCodeBackupIncomplete, "the backup could not capture every durable file")
			s.responses.Logger.WarnContext(r.Context(), "backup capture incomplete", "error", err)
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	filename := backup.ArchiveFilename(manifest)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("X-Backup-SHA256", manifest.ArchiveSHA256)
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	archive, err := os.Open(archivePath)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	defer func() { _ = archive.Close() }()
	info, err := archive.Stat()
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	http.ServeContent(w, r, filename, info.ModTime(), archive)
}

func unusedTempPath(dir, pattern string) (string, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return path, nil
}

func (s *Server) liveBackupStore() (db.DBTX, string, error) {
	path := strings.TrimSpace(s.storePath)
	if path == "" {
		return nil, "", errDurableStoreUnavailable
	}
	return s.database, path, nil
}

type restoreTarget struct {
	snapshot db.DBTX
	path     string
}

func (s *Server) restoreTarget() (restoreTarget, error) {
	if s.recovery != nil {
		path := strings.TrimSpace(s.storePath)
		if path == "" {
			return restoreTarget{}, errDurableStoreUnavailable
		}
		return restoreTarget{path: path}, nil
	}
	sqlDB, path, err := s.liveBackupStore()
	if err != nil {
		return restoreTarget{}, err
	}
	return restoreTarget{snapshot: sqlDB, path: path}, nil
}

func (s *Server) handleBackupCapabilities(w http.ResponseWriter, _ *http.Request) {
	expanded := backup.MaxExpandedArchiveBytes
	if expanded > math.MaxInt64 {
		s.responses.Fail(w, wire.ApiErrorCodeBackupUnavailable, "Backup transfer limits are invalid")
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.BackupCapabilities{
		FormatVersion: backup.FormatVersion, SchemaRevision: db.SchemaVersion,
		MaxArchiveBytes: backup.MaxArchiveBytes, MaxExpandedBytes: int64(expanded),
	})
}
