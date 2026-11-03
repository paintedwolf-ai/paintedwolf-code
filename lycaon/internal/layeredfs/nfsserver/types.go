package nfsserver

import (
	"errors"
	"os"
	"syscall"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	nfs "github.com/willscott/go-nfs"
)

// Default server bounds and protocol constants.
const (
	DefaultListenAddr        = "127.0.0.1:0"
	DefaultMaxRequestBytes   = 1 << 20 // 1 MiB maximum RPC record payload.
	DefaultMaxConcurrentRPCs = 32
	DefaultMaxQueuedRPCs     = 128
	DefaultMaxConnections    = 64
	DefaultMaxHandles        = 8192
	DefaultMaxDirListings    = 512
	DefaultReadHeaderTimeout = 5 * time.Second
	DefaultIdleTimeout       = 30 * time.Second

	// HandleSize is the fixed wire length of an authenticated Lycaon NFSv3 file handle.
	HandleSize = 56

	// RPC and program version constants (RFC 1813 / RFC 5531).
	rpcVersion2       = 2
	nfsProgramID      = 100003
	nfsVersion3       = 3
	mountProgramID    = 100005
	mountVersion3     = 3
	maxDirCookieBytes = 64 << 10

	// NFSStatusLoop maps symlink loops (ELOOP) to a distinct NFSv3 status code.
	NFSStatusLoop = nfs.NFSStatusMlink
)

// ViewID and RootID alias the string identifiers used by layeredfs.Core.
type (
	ViewID = string
	RootID = string
)

// Standard root identifiers for workspace exports.
const (
	ProjectRoot  RootID = "project"
	WorktreeRoot RootID = "worktree"
)

// WriteStability represents the RFC 1813 WRITE3 stable_how value.
type WriteStability uint32

const (
	// Unstable allows the server to buffer data in memory until COMMIT.
	Unstable WriteStability = 0
	// DataSync requires file data (and enough metadata to retrieve it) to be durable before replying.
	DataSync WriteStability = 1
	// FileSync requires all file data and metadata to be durable before replying.
	FileSync WriteStability = 2
)

// Sentinel errors returned by the NFSv3 server and protocol guards.
var (
	ErrNonLoopbackAddress = errors.New("layeredfs/nfsserver: listen address must be loopback")
	ErrUnauthorizedMount  = errors.New("layeredfs/nfsserver: unauthorized mount request")
	ErrUnauthorizedRPC    = errors.New("layeredfs/nfsserver: unauthorized RPC caller")
	ErrStaleHandle        = errors.New("layeredfs/nfsserver: stale file handle")
	ErrForgedHandle       = errors.New("layeredfs/nfsserver: forged or corrupted file handle")
	ErrCrossViewHandle    = errors.New("layeredfs/nfsserver: cross-view file handle rejected")
	ErrFrameTooLarge      = errors.New("layeredfs/nfsserver: RPC frame exceeds maximum size")
	ErrQueueFull          = errors.New("layeredfs/nfsserver: RPC request queue is full")
	ErrBadCookie          = errors.New("layeredfs/nfsserver: invalid or stale directory cookie")
	ErrReadOnlyExport     = errors.New("layeredfs/nfsserver: export is read-only")
)

// FaultInjector provides deterministic fault hooks for write, sync, and COMMIT testing.
type FaultInjector struct {
	OnWrite  func(viewID ViewID, path layeredfs.QualifiedPath, offset uint64, count int, stability WriteStability) error
	OnSync   func(viewID ViewID, path layeredfs.QualifiedPath) error
	OnCommit func(viewID ViewID, path layeredfs.QualifiedPath, offset uint64, count uint32) error
}

// Config configures the loopback NFSv3 and MOUNTv3 server.
type Config struct {
	ListenAddr        string
	MaxRequestBytes   int
	MaxConcurrentRPCs int
	MaxQueuedRPCs     int
	MaxConnections    int
	MaxHandles        int
	MaxDirListings    int
	ReadHeaderTimeout time.Duration
	IdleTimeout       time.Duration
	ExpectedUID       uint32
	ExpectedGID       uint32
	FaultInjector     *FaultInjector
}

func (c Config) withDefaults() Config {
	if c.ListenAddr == "" {
		c.ListenAddr = DefaultListenAddr
	}
	if c.MaxRequestBytes <= 0 {
		c.MaxRequestBytes = DefaultMaxRequestBytes
	}
	if c.MaxConcurrentRPCs <= 0 {
		c.MaxConcurrentRPCs = DefaultMaxConcurrentRPCs
	}
	if c.MaxQueuedRPCs <= 0 {
		c.MaxQueuedRPCs = DefaultMaxQueuedRPCs
	}
	if c.MaxConnections <= 0 {
		c.MaxConnections = DefaultMaxConnections
	}
	if c.MaxHandles <= 0 {
		c.MaxHandles = DefaultMaxHandles
	}
	if c.MaxDirListings <= 0 {
		c.MaxDirListings = DefaultMaxDirListings
	}
	if c.ReadHeaderTimeout <= 0 && c.IdleTimeout <= 0 {
		c.ReadHeaderTimeout = DefaultReadHeaderTimeout
		c.IdleTimeout = DefaultIdleTimeout
	} else if c.ReadHeaderTimeout <= 0 {
		c.ReadHeaderTimeout = c.IdleTimeout
	} else if c.IdleTimeout <= 0 {
		c.IdleTimeout = c.ReadHeaderTimeout
	}
	if c.ExpectedUID == 0 && c.ExpectedGID == 0 {
		c.ExpectedUID = uint32(os.Geteuid())
		c.ExpectedGID = uint32(os.Getegid())
	}
	return c
}

// ExportCapability binds a mounted view to an unguessable capability export path and caller UID.
type ExportCapability struct {
	ViewID     ViewID
	Root       RootID
	Token      string
	ExportPath string
	AllowedUID uint32
	AllowedGID uint32
	ReadOnly   bool
}

// authCred holds the parsed AUTH_SYS caller credentials for an RPC call.
type authCred struct {
	Flavor      nfs.AuthFlavor
	UID         uint32
	GID         uint32
	Groups      []uint32
	MachineName string
}

// MapErrorToNFSStatus translates layeredfs and OS/syscall errors to RFC 1813 status codes.
func MapErrorToNFSStatus(err error) nfs.NFSStatus {
	if err == nil {
		return nfs.NFSStatusOk
	}
	if status, ok := mapSentinelStatus(err); ok {
		return status
	}
	return mapSyscallStatus(err)
}

func mapSentinelStatus(err error) (nfs.NFSStatus, bool) {
	switch {
	case errors.Is(err, ErrBadCookie):
		return nfs.NFSStatusBadCookie, true
	case errors.Is(err, ErrQueueFull),
		errors.Is(err, layeredfs.ErrBarrierActive),
		errors.Is(err, layeredfs.ErrSourceChangedDuringCapture):
		return nfs.NFSStatusJukebox, true
	case errors.Is(err, ErrCrossViewHandle),
		errors.Is(err, ErrUnauthorizedMount),
		errors.Is(err, ErrUnauthorizedRPC),
		errors.Is(err, layeredfs.ErrPathEscape),
		errors.Is(err, layeredfs.ErrSymlinkEscape),
		errors.Is(err, layeredfs.ErrGitPending):
		return nfs.NFSStatusAccess, true
	case errors.Is(err, ErrStaleHandle),
		errors.Is(err, ErrForgedHandle),
		errors.Is(err, layeredfs.ErrStaleHandle),
		errors.Is(err, layeredfs.ErrViewReleased),
		errors.Is(err, layeredfs.ErrInvalidRoot):
		return nfs.NFSStatusStale, true
	case errors.Is(err, ErrReadOnlyExport):
		return nfs.NFSStatusROFS, true
	case errors.Is(err, layeredfs.ErrUnsupportedOperation):
		return nfs.NFSStatusNotSupp, true
	default:
		return 0, false
	}
}

func mapSyscallStatus(err error) nfs.NFSStatus {
	switch {
	case errors.Is(err, syscall.EPERM):
		return nfs.NFSStatusPerm
	case errors.Is(err, syscall.EACCES), errors.Is(err, os.ErrPermission):
		return nfs.NFSStatusAccess
	case errors.Is(err, syscall.ESTALE):
		return nfs.NFSStatusStale
	case errors.Is(err, syscall.ENOENT), errors.Is(err, os.ErrNotExist):
		return nfs.NFSStatusNoEnt
	case errors.Is(err, syscall.ENOTEMPTY):
		return nfs.NFSStatusNotEmpty
	case errors.Is(err, syscall.EEXIST), errors.Is(err, os.ErrExist):
		return nfs.NFSStatusExist
	case errors.Is(err, syscall.ENOTDIR):
		return nfs.NFSStatusNotDir
	case errors.Is(err, syscall.EISDIR):
		return nfs.NFSStatusIsDir
	case errors.Is(err, syscall.ELOOP):
		return NFSStatusLoop
	case errors.Is(err, syscall.ENOSPC):
		return nfs.NFSStatusNoSPC
	case errors.Is(err, syscall.EDQUOT):
		return nfs.NFSStatusDQuot
	case errors.Is(err, syscall.EFBIG):
		return nfs.NFSStatusFBig
	case errors.Is(err, syscall.EROFS):
		return nfs.NFSStatusROFS
	case errors.Is(err, syscall.EXDEV):
		return nfs.NFSStatusXDev
	case errors.Is(err, syscall.ENAMETOOLONG):
		return nfs.NFSStatusNameTooLong
	case errors.Is(err, syscall.ENOTSUP), errors.Is(err, syscall.EOPNOTSUPP):
		return nfs.NFSStatusNotSupp
	case errors.Is(err, syscall.EINVAL), errors.Is(err, os.ErrInvalid):
		return nfs.NFSStatusInval
	default:
		return nfs.NFSStatusIO
	}
}
