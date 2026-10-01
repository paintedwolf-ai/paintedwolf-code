// Package backup manages durable-install archives and restore transactions.
package backup

import (
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/localdata"
)

const (
	// FormatVersion is the current archive manifest format.
	FormatVersion = 1
)

var (
	// MaxArchiveBytes bounds one compressed backup archive.
	MaxArchiveBytes = archiveLimits.CompressedBytes
	// MaxExpandedArchiveBytes bounds one archive's extracted payload.
	MaxExpandedArchiveBytes = archiveLimits.ExpandedBytes
	maxManifestBytes        = archiveLimits.ManifestBytes
	maxArchiveEntries       = archiveLimits.Entries
	maxSymlinkTargetBytes   = archiveLimits.SymlinkTargetBytes
)

// storeRelPath identifies the database that requires a live snapshot.
const storeRelPath = "store.db"

// PendingMarkerName is the config-root relative restore-pending marker.
const PendingMarkerName = "restore.pending.json"

const (
	manifestEntryName  = "manifest.json"
	snapshotTempPrefix = ".backup-snapshot-"
)

// Manifest is the top-level archive metadata.
// SchemaShapeDigest identifies the exact store shape.
// AppVersion is display provenance only.
type Manifest struct {
	// ArchiveSHA256 is the completed ZIP digest, returned to the transport only.
	ArchiveSHA256     string   `json:"-"`
	FormatVersion     int      `json:"format_version"`
	AppVersion        string   `json:"app_version"`
	SchemaUserVersion int      `json:"schema_user_version"`
	SchemaShapeDigest string   `json:"schema_shape_digest"`
	CreatedAt         string   `json:"created_at"`
	ReplaceRelPaths   []string `json:"replace_rel_paths"`
	// ReplaceRelDirs are restored as complete directory snapshots.
	ReplaceRelDirs      []string    `json:"replace_rel_dirs,omitempty"`
	RetainedBranchTrees []string    `json:"retained_branch_trees,omitempty"`
	Files               []FileEntry `json:"files"`
}

// FileEntry is one archived file's integrity record.
type FileEntry struct {
	RelPath string `json:"rel_path"`
	Kind    string `json:"kind"`
	Mode    uint32 `json:"mode"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
}

// PendingOperation identifies the staged transaction.
type PendingOperation string

const (
	// PendingOperationRestore applies an archive or recovery snapshot.
	PendingOperationRestore PendingOperation = "restore"
	// PendingOperationFreshStart installs a clean store.
	PendingOperationFreshStart PendingOperation = "fresh_start"
)

// PendingMarker records a validated restore awaiting application.
// SourceAppVersion is provenance; the schema determines compatibility.
type PendingMarker struct {
	LiveStoreFilename   string           `json:"live_store_filename"`
	Operation           PendingOperation `json:"operation"`
	StagingDir          string           `json:"staging_dir"`
	RecoveryDir         string           `json:"recovery_dir"`
	SourceAppVersion    string           `json:"source_app_version"`
	CreatedAt           string           `json:"created_at"`
	Files               []PendingFile    `json:"files"`
	DeleteRelPaths      []string         `json:"delete_rel_paths,omitempty"`
	ReplaceRelDirs      []string         `json:"replace_rel_dirs,omitempty"`
	RetainedBranchTrees []string         `json:"retained_branch_trees,omitempty"`
	Applied             []string         `json:"applied,omitempty"`
	// FailedAt permits a subsequent restore to replace this failed transaction.
	FailedAt      string `json:"failed_at,omitempty"`
	FailureDetail string `json:"failure_detail,omitempty"`
}

// PendingFile is one staged path awaiting apply.
type PendingFile struct {
	RelPath string `json:"rel_path"`
	Kind    string `json:"kind"`
	Mode    uint32 `json:"mode"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

const (
	fileKindRegular = "file"
	fileKindSymlink = "symlink"
)

// StageResult is returned after a successful stage (before restart).
type StageResult struct {
	RestartRequired bool
	// RecoveryCopyPath holds the restore transaction's live pre-image.
	RecoveryCopyPath string
	// ReclaimedPreImages names pre-images from completed transactions this
	// stage removed, and the bytes each returned.
	ReclaimedPreImages []localdata.RestorePreImage
	// SupersededTransaction identifies the retained recovery copy of a replaced transaction.
	SupersededTransaction string
}

var (
	ErrInvalid          = errors.New("backup archive invalid")
	ErrBaselineMismatch = errors.New("backup schema does not match this app")
	ErrPending          = errors.New("a backup restore is already pending restart")
)

// ErrCaptureIncomplete means the durable file set could not be captured.
var ErrCaptureIncomplete = errors.New("backup files are missing, unreadable, or changed during capture")

// InvalidError wraps ErrInvalid with detail.
type InvalidError struct {
	Detail string
}

func (e *InvalidError) Error() string {
	if e == nil || e.Detail == "" {
		return ErrInvalid.Error()
	}
	return fmt.Sprintf("%s: %s", ErrInvalid.Error(), e.Detail)
}

func (e *InvalidError) Unwrap() error { return ErrInvalid }

// BaselineMismatchError reports differing schema baselines. ShapeDetail names
// the structural difference when the fixed marker matched and the shape did not.
type BaselineMismatchError struct {
	ArchiveVersion int
	BinaryVersion  int
	ShapeDetail    string
}

func (e *BaselineMismatchError) Error() string {
	if e == nil {
		return ErrBaselineMismatch.Error()
	}
	if e.ShapeDetail != "" {
		return fmt.Sprintf("%s (schema shape differs: %s)", ErrBaselineMismatch.Error(), e.ShapeDetail)
	}
	return fmt.Sprintf("%s (archive schema_user_version=%d, app SchemaVersion=%d)",
		ErrBaselineMismatch.Error(), e.ArchiveVersion, e.BinaryVersion)
}

func (e *BaselineMismatchError) Unwrap() error { return ErrBaselineMismatch }
