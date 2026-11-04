// Package fuselinux implements the Linux unprivileged FUSE3 worker workspace adapter (#1659).
//
// It mounts an engine-served FUSE v7.31 filesystem onto a user-owned directory
// under the worker runtime root using fusermount3 without allow_other, root
// elevation, or /etc/fuse.conf edits.
package fuselinux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
)

const (
	// DefaultFusermountBin is the standard unprivileged FUSE3 mount helper binary on Linux.
	DefaultFusermountBin = "/usr/bin/fusermount3"
	// DefaultFuseDevicePath is the Linux kernel FUSE character device path.
	DefaultFuseDevicePath = "/dev/fuse"
	// DefaultSubtype is the FUSE filesystem subtype reported in /proc/self/mountinfo.
	DefaultSubtype = "lycaon_layeredfs"
	// DefaultCachePolicy states the kernel cache contract. Entries and attributes revalidate on
	// every use. Writes are write-through. Page cache is dropped at open: a descriptor already
	// open keeps its pages until notify_inval or a size change.
	DefaultCachePolicy = "entry_valid=0,attr_valid=0,writeback_cache=0,fopen_keep_cache=0,notify_inval"
	// DefaultMaxReadAhead is the negotiated FUSE max_readahead size (128 KiB).
	DefaultMaxReadAhead = 131072
	// DefaultMaxWrite is the negotiated FUSE max_write payload size (128 KiB).
	DefaultMaxWrite = 131072
	// FUSEProtocolMajor is the pinned Linux FUSE kernel ABI major version.
	FUSEProtocolMajor = 7
	// FUSEProtocolMinor is the pinned Linux FUSE kernel ABI minor version.
	FUSEProtocolMinor = 31
)

// Config configures the Linux FUSE worker workspace adapter.
type Config struct {
	HostOS                string
	FusermountBin         string
	FuseDevicePath        string
	MountTimeout          time.Duration
	MaxConcurrentRequests int
	MaxLockWaiters        int
	ConfinementCheckFn    func(ctx context.Context, probeCfg adapter.ProbeConfig) error
	MountHookForTest      func(viewID, rootID, mountDir string) error
	ClientSyncFn          func(path string) error
	RemoveAllFn           func(path string) error
	OnMutationHook        func(opcode uint32, rel string)
	OnLockWait            func(unique, start, end uint64, waiting bool)
}

// MountFacts records the concrete mount options, kernel mountinfo identity, and negotiated protocol capabilities.
type MountFacts struct {
	ViewID, RootID, MountID, ConnectionID string
	MountPath, FSName, Subtype            string
	FilesystemType, DevMajorMinor         string
	MountOptions, SuperOptions            string
	ProtocolMajor, ProtocolMinor          uint32
	KernelMinor, GrantedFlags             uint32
	MaxReadAhead, MaxWrite                uint32
	CachePolicy                           string
	ParentDev, MountedDev                 uint64
	OwnerUID, OwnerGID                    uint32
}

type rootMount struct {
	grant        adapter.RootGrant
	mountPath    string
	server       *Server
	facts        MountFacts
	serverDead   atomic.Bool
	unmounted    atomic.Bool
	serverClosed atomic.Bool
}

type viewState struct {
	view            *adapter.PreparedView
	core            *layeredfs.Core
	primaryRoot     string
	mounts          map[string]*rootMount
	denySourcePaths []string
	quiesceHook     func(ctx context.Context, viewID string) error
	flushHook       func(ctx context.Context, viewID string) error
	privateGitMgr   *privategit.Manager
	dirtyMu         sync.Mutex
	dirtyPaths      map[string]map[string]struct{}
	lastFilesSynced int
	lastDirsSynced  int
	accessCount     atomic.Int64
	releasing       atomic.Bool
	coreReleased    atomic.Bool
	runtimeCleaned  atomic.Bool
}

var _ adapter.Adapter = (*Adapter)(nil)

// Adapter implements adapter.Adapter for Linux engine-served FUSE3 worker mounts (#1659).
type Adapter struct {
	cfg   Config
	facts adapter.AdapterFacts
	mu    sync.RWMutex
	views map[string]*viewState
}

// New constructs a Linux FUSE worker workspace adapter.
func New(cfg Config) *Adapter {
	if cfg.HostOS == "" {
		cfg.HostOS = runtime.GOOS
	}
	if cfg.FusermountBin == "" {
		if resolved, err := execpkg.LookPath("fusermount3"); err == nil && resolved != "" {
			cfg.FusermountBin = resolved
		} else {
			cfg.FusermountBin = DefaultFusermountBin
		}
	}
	if cfg.FuseDevicePath == "" {
		cfg.FuseDevicePath = DefaultFuseDevicePath
	}
	if cfg.MountTimeout <= 0 {
		cfg.MountTimeout = 10 * time.Second
	}
	return &Adapter{
		cfg: cfg,
		facts: adapter.AdapterFacts{
			AdapterID:                 adapter.AdapterFUSELinux,
			PreparationKind:           adapter.PreparationMounted,
			UntouchedSourceVisibility: adapter.VisibilityLive,
			CacheRevalidation: adapter.CacheRevalidation{
				Mode:                 DefaultCachePolicy,
				ExplicitInvalidation: true,
				AttributeTTL:         0,
			},
			MetadataCapabilities: adapter.MetadataCapabilities{
				SupportsSymlinks: true, SupportsHardLinks: true, SupportsXattrs: true,
				SupportsChmod: true, SupportsTimestamps: true, SupportsPOSIXLocks: true, SupportsFlock: true,
			},
			SupportsPrivateGit:        true,
			SupportsCheckpointBarrier: true,
		},
		views: make(map[string]*viewState),
	}
}

// NoopQuiesceHook is a QuiesceHook for callers that have already stopped worker writers on the host.
func NoopQuiesceHook(_ context.Context, _ string) error { return nil }

// NoWritableMappingsFlushHook is a FlushClientHook for callers that hold no writable client mmap regions.
func NoWritableMappingsFlushHook(_ context.Context, _ string) error { return nil }

// DetachMountEntry rewrites cmd so nothing on a mount is touched before its execve. The server
// runs inside the engine, and between fork and exec Go's vfork child holds a scheduler slot that a
// stop-the-world waits for, and that is the only slot under GOMAXPROCS=1. A chdir into the mount,
// or executing a program from it, needs a server reply at that point, which a server that cannot
// be scheduled cannot give: the whole engine hangs.
// Every process the engine itself starts in a view must go through this. Entering the mount after
// exec, from a helper outside it, leaves the parent free.
func DetachMountEntry(cmd *exec.Cmd) {
	dir := cmd.Dir
	if dir == "" {
		dir = "."
	}
	args := append([]string{"/bin/sh", "-c", `cd "$1" && shift && exec "$@"`, "sh", dir, cmd.Path}, cmd.Args[1:]...)
	cmd.Path, cmd.Args, cmd.Dir = "/bin/sh", args, ""
}

// ID returns the canonical adapter identifier (adapter.AdapterFUSELinux).
func (a *Adapter) ID() adapter.AdapterID { return adapter.AdapterFUSELinux }

// Name returns the canonical adapter identifier (adapter.AdapterFUSELinux).
func (a *Adapter) Name() adapter.AdapterID { return adapter.AdapterFUSELinux }

// Facts returns the static capability and coherence facts for fuselinux.
func (a *Adapter) Facts() adapter.AdapterFacts { return a.facts }

// MountFactsForView returns the recorded MountFacts for the primary root of viewID.
func (a *Adapter) MountFactsForView(viewID string) (MountFacts, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if st, ok := a.views[viewID]; ok {
		if m := st.mounts[st.primaryRoot]; m != nil {
			return m.facts, true
		}
	}
	return MountFacts{}, false
}

// ServerForView returns the primary *Server for viewID, or nil if unknown.
func (a *Adapter) ServerForView(viewID string) *Server {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if st, ok := a.views[viewID]; ok {
		if m := st.mounts[st.primaryRoot]; m != nil {
			return m.server
		}
	}
	return nil
}

// CoreForView returns the *layeredfs.Core for viewID, or nil if unknown.
func (a *Adapter) CoreForView(viewID string) *layeredfs.Core {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if st, ok := a.views[viewID]; ok {
		return st.core
	}
	return nil
}

// Probe verifies Linux platform support, confinement availability, /dev/fuse access,
// fusermount3 availability, unprivileged FUSE mount/read/write/shell coherence, and cleanup.
func (a *Adapter) Probe(ctx context.Context, probeCfg adapter.ProbeConfig) (adapter.ProbeResult, error) {
	res := adapter.ProbeResult{AdapterID: adapter.AdapterFUSELinux, Attempts: 1}
	if err := ctx.Err(); err != nil {
		res.ReasonCode, res.Diagnostic = adapter.ReasonContextCanceled, err.Error()
		return res, err
	}
	if err := a.preflightProbeChecks(ctx, probeCfg, &res); err != nil {
		return res, err
	}

	probeBase, cleanupBase, err := prepareProbeBaseDir(probeCfg.RuntimeDir)
	if err != nil {
		res.ReasonCode, res.Diagnostic = adapter.ReasonFUSEMountDenied, err.Error()
		return res, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: err}
	}
	defer cleanupBase()

	trunkDir, backingDir, runtimeDir := filepath.Join(probeBase, "trunk"), filepath.Join(probeBase, "backing"), filepath.Join(probeBase, "runtime")
	for _, d := range []string{trunkDir, backingDir, runtimeDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			res.ReasonCode, res.Diagnostic = adapter.ReasonFUSEMountDenied, err.Error()
			return res, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: err}
		}
	}
	if err := os.WriteFile(filepath.Join(trunkDir, "probe.txt"), []byte("seed\n"), 0o644); err != nil {
		res.ReasonCode, res.Diagnostic = adapter.ReasonFUSEMountDenied, err.Error()
		return res, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: err}
	}

	probeViewID := fmt.Sprintf("probe-fuselinux-%d", time.Now().UnixNano())
	prep, pErr := a.Prepare(ctx, adapter.PrepareRequest{
		ViewID: probeViewID, BackingDir: backingDir, RuntimeDir: runtimeDir,
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
	})
	if pErr != nil {
		res.ReasonCode, res.Diagnostic = classifyProbeError(pErr), pErr.Error()
		return res, pErr
	}
	res.MountExercised = true
	defer func() { _, _ = a.Release(context.Background(), probeViewID) }()

	probeFile := filepath.Join(prep.PrimaryWorkPath, "probe.txt")
	if raw, rErr := os.ReadFile(probeFile); rErr != nil || string(raw) != "seed\n" {
		res.ReasonCode, res.Diagnostic = adapter.ReasonProbeReadWriteFailed, fmt.Sprintf("probe read failed: %v", rErr)
		return res, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: rErr}
	}
	res.ReadExercised = true

	if err := os.WriteFile(probeFile, []byte("coherent-probe\n"), 0o644); err != nil {
		res.ReasonCode, res.Diagnostic = adapter.ReasonProbeReadWriteFailed, err.Error()
		return res, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: err}
	}
	// The shell enters the mount itself, after exec: see DetachMountEntry.
	out, exitCode, cErr := execpkg.Run(ctx, "/bin/sh", []string{"-c", `cd "$1" && exec cat probe.txt`, "sh", prep.PrimaryWorkPath}, execpkg.ExecOpts{Launch: execpkg.HostLaunch("fuse_probe")})
	if cErr != nil || exitCode != 0 || strings.TrimSpace(string(out)) != "coherent-probe" {
		res.ReasonCode, res.Diagnostic = adapter.ReasonProbeReadWriteFailed, fmt.Sprintf("shell coherence check failed: exit=%d err=%v out=%q", exitCode, cErr, string(out))
		return res, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: cErr}
	}
	res.WriteExercised = true

	if trunkRaw, tErr := os.ReadFile(filepath.Join(trunkDir, "probe.txt")); tErr != nil || string(trunkRaw) != "seed\n" {
		res.ReasonCode, res.Diagnostic = adapter.ReasonBackingConfinementFailed, "trunk file mutated during probe"
		return res, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic}
	}

	if _, rErr := a.Release(ctx, probeViewID); rErr != nil {
		res.ReasonCode, res.Diagnostic = adapter.ReasonCleanupFailed, rErr.Error()
		return res, rErr
	}
	res.CleanedUp, res.Supported = true, true
	return res, nil
}
