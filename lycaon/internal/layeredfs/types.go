// Package layeredfs implements the platform-neutral layered filesystem core
// and mutation journal for worker views (#1657).
package layeredfs

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"syscall"
	"time"
)

// FormatVersion is the durable mutation journal schema version.
const FormatVersion = 1

var (
	ErrViewReleased               = errors.New("layeredfs: view has been released")
	ErrStaleHandle                = errors.New("layeredfs: stale or invalid handle")
	ErrPathEscape                 = errors.New("layeredfs: path escapes root boundary")
	ErrSymlinkEscape              = errors.New("layeredfs: symlink or directory replacement escapes root boundary")
	ErrSourceChangedDuringCapture = errors.New("layeredfs: source changed during coherent base capture")
	ErrGitPending                 = errors.New("layeredfs: private git initialization is pending")
	ErrBarrierActive              = errors.New("layeredfs: checkpoint barrier is active")
	ErrInvalidRoot                = errors.New("layeredfs: invalid or unknown root")
	ErrUnsupportedOperation       = errors.New("layeredfs: operation not supported")
)

type NodeID uint64
type Generation uint64
type HandleID uint64
type LayerSource string

const (
	LayerPrivate LayerSource = "private"
	LayerParent  LayerSource = "parent"
	LayerTrunk   LayerSource = "trunk"
	LayerGit     LayerSource = "git"
)

type EntryKind string

const (
	EntryAbsent    EntryKind = "absent"
	EntryRegular   EntryKind = "regular"
	EntryDirectory EntryKind = "directory"
	EntrySymlink   EntryKind = "symlink"
	EntryFIFO      EntryKind = "fifo"
	EntrySocket    EntryKind = "socket"
)

// IsSpecial reports a rendezvous node. Its state lives in the kernel, so it exists in a live view
// but has no content: checkpoints and promotion treat its path as absent.
func (k EntryKind) IsSpecial() bool { return k == EntryFIFO || k == EntrySocket }

type QualifiedPath struct {
	RootID string `json:"root_id"`
	Rel    string `json:"rel"`
}

type RootSpec struct {
	ID        string
	Label     string
	TrunkPath string
	Subroot   string
	IsPrimary bool
}

type ParentEntry struct {
	Kind       EntryKind         `json:"kind"`
	Mode       fs.FileMode       `json:"mode"`
	Size       int64             `json:"size"`
	ModTime    time.Time         `json:"mod_time"`
	SHA256     string            `json:"sha256,omitempty"`
	LinkTarget string            `json:"link_target,omitempty"`
	Xattrs     map[string][]byte `json:"xattrs,omitempty"`
	LinkGroup  string            `json:"link_group,omitempty"`
	BackingAbs string            `json:"-"`
}

type ParentLayer interface {
	ID() string
	Lookup(qpath QualifiedPath) (ParentEntry, bool, error)
	OpenContent(qpath QualifiedPath) (*osFileReadHandle, error)
	ListDir(qpath QualifiedPath) (map[string]ParentEntry, error)
}

type LowerProvider interface {
	Lstat(rel string) (LowerEntry, error)
	ReadDir(rel string) ([]LowerDirEntry, error)
	OpenRead(rel string) (ReadSeekCloser, LowerEntry, error)
	Readlink(rel string) (string, error)
	ListXattr(rel string) (map[string][]byte, error)
}

type ReadSeekCloser interface {
	Read([]byte) (int, error)
	ReadAt([]byte, int64) (int, error)
	Seek(int64, int) (int64, error)
	Close() error
}

type LowerEntry struct {
	Kind       EntryKind
	Mode       fs.FileMode
	Size       int64
	ModTime    time.Time
	Dev        uint64
	Ino        uint64
	CtimeSec   int64
	CtimeNsec  int64
	UID        uint32
	GID        uint32
	Nlink      uint32
	LinkTarget string
}

type LowerDirEntry struct {
	Name string
	Kind EntryKind
	Mode fs.FileMode
	Ino  uint64
}

type GitInterceptOutcome struct {
	Intercepted  bool
	RedirectRoot string
	RedirectRel  string
}

type GitInterceptor interface {
	InterceptGitAccess(viewID string, qpath QualifiedPath, writeIntent bool) (GitInterceptOutcome, error)
}

type ObservationKind string

const (
	ObserveLookup   ObservationKind = "lookup"
	ObserveStat     ObservationKind = "stat"
	ObserveRead     ObservationKind = "read"
	ObserveReadDir  ObservationKind = "readdir"
	ObserveReadlink ObservationKind = "readlink"
)

type ObservationEvent struct {
	ViewID     string
	Path       QualifiedPath
	Layer      LayerSource
	NodeID     NodeID
	Generation Generation
	Kind       ObservationKind
	Size       int64
	ModTimeNS  int64
	SHA256     string
	Ino        uint64
	Dev        uint64
}

type ObservationSink interface {
	RecordObservation(ev ObservationEvent)
}

// FaultHooks provides deterministic test hooks for concurrency barriers and fault injection.
type FaultHooks struct {
	BeforeAppendLock       func(qpath QualifiedPath, data []byte)
	WriteAtFunc            func(qpath QualifiedPath, file *os.File, data []byte, offset int64) (int, error)
	LowerXattrHook         func(qpath QualifiedPath) error
	WorkingFileHashReader  func(qpath QualifiedPath, r io.Reader) (io.Reader, error)
	JournalSyncHook        func(phase string) error
	ReleaseWaitHook        func(stage string)
	NamespacePreCommitHook func(op string, qpath QualifiedPath)
}

type Config struct {
	ViewID              string
	BackingDir          string
	Roots               []RootSpec
	ActiveRootID        string
	ParentLayers        []ParentLayer
	GitInterceptor      GitInterceptor
	GitAllowPassthrough bool
	ObservationSink     ObservationSink
	MaxCaptureRetries   int
	CaptureRetryHook    func(qpath QualifiedPath, attempt int)
	LookupRaceHook      func(qpath QualifiedPath, component string)
	FaultHooks          *FaultHooks
}

// EntryAttr carries unified attributes returned by Stat/Lstat/GetAttr.
type EntryAttr struct {
	NodeID     NodeID
	OriginID   NodeID
	Generation Generation
	Layer      LayerSource
	Kind       EntryKind
	Mode       fs.FileMode
	Size       int64
	ModTime    time.Time
	ATime      time.Time
	CTime      time.Time
	Nlink      uint32
	LinkTarget string
	Xattrs     map[string][]byte
}

type DirEntry struct {
	Name       string
	NodeID     NodeID
	OriginID   NodeID
	Generation Generation
	Kind       EntryKind
	Mode       fs.FileMode
	Size       int64
	ModTime    time.Time
	Layer      LayerSource
}

// StableID is the identity a protocol handle or inode number should carry: a copied-up node keeps
// the identity of the lower object it came from.
func (a EntryAttr) StableID() NodeID {
	if a.OriginID != 0 {
		return a.OriginID
	}
	return a.NodeID
}

// StableID is the identity of a listed entry, matching EntryAttr.StableID for the same object.
func (e DirEntry) StableID() NodeID {
	if e.OriginID != 0 {
		return e.OriginID
	}
	return e.NodeID
}

// BackingCapacity is the capacity of the filesystem holding private working storage.
type BackingCapacity struct {
	TotalBytes, FreeBytes, AvailableBytes, TotalFiles, FreeFiles uint64
}

type Stats struct {
	LowerDirReads     int64
	LowerLookups      int64
	PhysicalCopyUps   int64
	LogicalChanges    int64
	CapturedBaseBytes int64
	JournalEntries    int64
	Flushes           int64
}

type OpenFlags int

const (
	OpenReadOnly  OpenFlags = 0
	OpenWriteOnly OpenFlags = 1 << 0
	OpenReadWrite OpenFlags = 1 << 1
	OpenCreate    OpenFlags = 1 << 2
	OpenExclusive OpenFlags = 1 << 3
	OpenTruncate  OpenFlags = 1 << 4
	OpenAppend    OpenFlags = 1 << 5
	OpenNoFollow  OpenFlags = 1 << 6
	OpenDirectory OpenFlags = 1 << 7
)

func (f OpenFlags) hasWriteIntent() bool {
	return f&(OpenWriteOnly|OpenReadWrite|OpenCreate|OpenTruncate|OpenAppend) != 0
}

type SetAttrRequest struct {
	Mode     *fs.FileMode
	UID      *uint32
	GID      *uint32
	Size     *int64
	ATime    *time.Time
	MTime    *time.Time
	NoFollow bool
}

type CapabilityLimits struct {
	SupportsSymlinks   bool
	SupportsHardLinks  bool
	SupportsXattrs     bool
	SupportsChmod      bool
	SupportsTimestamps bool
	SupportsChown      bool
	MaxSymlinkHops     int
}

func DefaultCapabilities() CapabilityLimits {
	return CapabilityLimits{
		SupportsSymlinks:   true,
		SupportsHardLinks:  true,
		SupportsXattrs:     true,
		SupportsChmod:      true,
		SupportsTimestamps: true,
		SupportsChown:      false,
		MaxSymlinkHops:     16,
	}
}

func MapErrno(err error) syscall.Errno {
	if err == nil {
		return 0
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno
	}
	switch {
	case errors.Is(err, ErrStaleHandle), errors.Is(err, ErrViewReleased):
		return syscall.ESTALE
	case errors.Is(err, ErrPathEscape), errors.Is(err, ErrSymlinkEscape), errors.Is(err, ErrGitPending):
		return syscall.EACCES
	case errors.Is(err, ErrSourceChangedDuringCapture), errors.Is(err, ErrBarrierActive):
		return syscall.EAGAIN
	case errors.Is(err, ErrUnsupportedOperation):
		return syscall.ENOTSUP
	case errors.Is(err, fs.ErrNotExist):
		return syscall.ENOENT
	case errors.Is(err, fs.ErrExist):
		return syscall.EEXIST
	case errors.Is(err, fs.ErrPermission):
		return syscall.EACCES
	case errors.Is(err, fs.ErrInvalid):
		return syscall.EINVAL
	default:
		return syscall.EIO
	}
}
