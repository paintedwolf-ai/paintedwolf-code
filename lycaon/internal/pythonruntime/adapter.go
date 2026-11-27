package pythonruntime

import (
	"context"
	"fmt"
	"io/fs"
	"time"
)

// Namespace identifies the distinct filesystem domains exposed to a guest.
type Namespace string

const (
	NamespaceRuntime    Namespace = "runtime"
	NamespacePackages   Namespace = "packages"
	NamespaceScratch    Namespace = "scratch"
	NamespaceWorkspace  Namespace = "workspace"
	NamespaceNullDevice Namespace = "null_device"
)

// CanonicalTarget uniquely identifies a resource across namespaces, roots,
// worker branches, and document/entry identities.
type CanonicalTarget struct {
	Namespace  Namespace `json:"namespace"`
	RootID     string    `json:"root_id,omitempty"`
	Branch     string    `json:"branch,omitempty"`
	RelPath    string    `json:"rel_path"`
	DocumentID string    `json:"document_id,omitempty"`
	EntryID    string    `json:"entry_id,omitempty"`
}

// Errno represents a POSIX/WASI-compatible filesystem error returned across
// the byte-oriented guest descriptor boundary.
type Errno uint32

const (
	ErrnoSuccess Errno = iota
	ErrnoENOENT
	ErrnoEEXIST
	ErrnoEACCES
	ErrnoEPERM
	ErrnoEISDIR
	ErrnoENOTDIR
	ErrnoEXDEV
	ErrnoEINVAL
	ErrnoEBADF
	ErrnoENOSPC
	ErrnoEROFS
	ErrnoEIO
)

// String returns the symbolic POSIX/WASI errno name.
func (e Errno) String() string {
	switch e {
	case ErrnoSuccess:
		return "SUCCESS"
	case ErrnoENOENT:
		return "ENOENT"
	case ErrnoEEXIST:
		return "EEXIST"
	case ErrnoEACCES:
		return "EACCES"
	case ErrnoEPERM:
		return "EPERM"
	case ErrnoEISDIR:
		return "EISDIR"
	case ErrnoENOTDIR:
		return "ENOTDIR"
	case ErrnoEXDEV:
		return "EXDEV"
	case ErrnoEINVAL:
		return "EINVAL"
	case ErrnoEBADF:
		return "EBADF"
	case ErrnoENOSPC:
		return "ENOSPC"
	case ErrnoEROFS:
		return "EROFS"
	case ErrnoEIO:
		return "EIO"
	default:
		return fmt.Sprintf("ERRNO(%d)", uint32(e))
	}
}

// Error implements the error interface for non-zero Errno values.
func (e Errno) Error() string {
	return e.String()
}

// OpenFlags specifies guest file open intent across the descriptor boundary.
type OpenFlags uint32

const (
	OpenRead OpenFlags = 1 << iota
	OpenWrite
	OpenCreate
	OpenExclusive
	OpenTruncate
	OpenAppend
	OpenDirectory
	OpenNoFollow
)

// OpenRequest describes a byte-oriented guest file open crossing into Go.
type OpenRequest struct {
	Path        string
	Flags       OpenFlags
	Mode        fs.FileMode
	OriginFrame *DeclaredFrame
}

// StatInfo carries guest-visible file metadata along with its canonical target.
type StatInfo struct {
	Target    CanonicalTarget
	Name      string
	Size      int64
	Mode      fs.FileMode
	ModTime   time.Time
	IsDir     bool
	IsSymlink bool
	Inode     uint64
}

// DirEntry describes a single entry returned by WorkspaceAdapter.ReadDir.
type DirEntry struct {
	Name      string
	IsDir     bool
	IsSymlink bool
	Mode      fs.FileMode
}

// PublishedChange records one surviving workspace mutation committed during
// clean-success settlement.
type PublishedChange struct {
	Target            CanonicalTarget  `json:"target"`
	Operation         string           `json:"operation"`
	PreviousTarget    *CanonicalTarget `json:"previous_target,omitempty"`
	BaseRevision      string           `json:"base_revision,omitempty"`
	CommittedRevision string           `json:"committed_revision,omitempty"`
	BytesWritten      int64            `json:"bytes_written,omitempty"`
}

// RevisionConflict records a canonical target whose revision changed between
// earliest base pin and settlement.
type RevisionConflict struct {
	Target           CanonicalTarget `json:"target"`
	ExpectedRevision string          `json:"expected_revision"`
	ActualRevision   string          `json:"actual_revision"`
}

// SettlementReport summarizes clean-success publication or failure discard of
// the per-program workspace overlay.
type SettlementReport struct {
	CleanCompletion     bool               `json:"clean_completion"`
	Published           []PublishedChange  `json:"published,omitempty"`
	Discarded           []CanonicalTarget  `json:"discarded,omitempty"`
	Conflicts           []RevisionConflict `json:"conflicts,omitempty"`
	PartialFailure      error              `json:"-"`
	PartialFailureError string             `json:"partial_failure_error,omitempty"`
}

// GuestFile is the byte-oriented open descriptor handle returned by
// WorkspaceAdapter.Open. CPython handles text codecs and buffering above it.
type GuestFile interface {
	Read(p []byte) (int, Errno)
	Write(p []byte) (int, Errno)
	Seek(offset int64, whence int) (int64, Errno)
	Truncate(size int64) Errno
	Sync() Errno
	Stat() (StatInfo, Errno)
	Close() Errno
}

// WorkspaceAdapter mediates all guest filesystem operations and per-program
// overlay settlement (implemented by PY03).
type WorkspaceAdapter interface {
	Open(ctx context.Context, req OpenRequest) (GuestFile, Errno)
	Stat(ctx context.Context, path string) (StatInfo, Errno)
	Lstat(ctx context.Context, path string) (StatInfo, Errno)
	ReadDir(ctx context.Context, path string) ([]DirEntry, Errno)
	Mkdir(ctx context.Context, path string, perm fs.FileMode) Errno
	Unlink(ctx context.Context, path string) Errno
	Rmdir(ctx context.Context, path string) Errno
	// Rename moves oldPath to newPath within the same mount, returning
	// ErrnoEXDEV for unsupported cross-mount renames.
	Rename(ctx context.Context, oldPath, newPath string) Errno
	Readlink(ctx context.Context, path string) (string, Errno)
	// Settle publishes surviving workspace changes when cleanCompletion is
	// true, or discards unpublished overlay state on failure, stop, or trap.
	Settle(ctx context.Context, cleanCompletion bool) (*SettlementReport, error)
}
