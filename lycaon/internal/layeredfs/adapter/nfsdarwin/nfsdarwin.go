//go:build darwin

// Package nfsdarwin implements the macOS built-in NFSv3 loopback worker mount adapter (#1661).
//
// It mounts an engine-served NFSv3 loopback export (127.0.0.1:<exportPath>) onto a
// user-owned directory under the worker runtime/storage root using /sbin/mount_nfs
// without requiring macFUSE, FSKit, or system extension approval.
package nfsdarwin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/nfsserver"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
)

const (
	// DefaultMountNFSBin is the built-in macOS NFS mount helper binary.
	DefaultMountNFSBin = "/sbin/mount_nfs"
	// DefaultUmountBin is the built-in macOS unmount binary.
	DefaultUmountBin = "/sbin/umount"
	// DefaultDiskutilBin is the built-in macOS diskutil binary used as a last-resort forced unmount fallback.
	DefaultDiskutilBin = "/usr/sbin/diskutil"

	// DefaultRSize is the NFSv3 read payload size (64 KiB).
	DefaultRSize = 65536
	// DefaultWSize is the NFSv3 write payload size (64 KiB).
	DefaultWSize = 65536
	// DefaultRetrans is the soft mount retransmission count.
	DefaultRetrans = 2
	// DefaultTimeo is the soft mount timeout in deciseconds (1.0s).
	DefaultTimeo = 10
	// DefaultCachePolicy is the explicit cache option set: no attribute or negative-name caching, and
	// no READDIRPLUS. With READDIRPLUS the client answers a lookup from the last listing, which omits
	// the ._ files that present held attributes, and then refuses to create the one it needs.
	DefaultCachePolicy = "actimeo=0,acregmin=0,acregmax=0,acdirmin=0,acdirmax=0,nonegnamecache,nordirplus"

	// unmountWaitBound limits how long one unmount attempt may hold Release. A helper stuck in the
	// kernel cannot be killed, so the wait has to end on its own.
	unmountWaitBound = 5 * time.Second
)

// Config configures the macOS NFSv3 loopback adapter.
type Config struct {
	HostOS        string
	MountNFSBin   string
	UmountBin     string
	DiskutilBin   string
	MountTimeout  time.Duration
	MaxHandles    int
	FaultInjector *nfsserver.FaultInjector
	ClientSyncFn  func(path string) error
	RemoveAllFn   func(path string) error
	// ConfinementCheckFn proves direct backing writes are denied in the worker's context. Without
	// it Probe reports confinement unavailable rather than claiming a denial nothing established.
	ConfinementCheckFn func(ctx context.Context, probeCfg adapter.ProbeConfig) error
}

// MountFacts records the concrete mount options, ports, and export identity for a mounted root.
type MountFacts struct {
	ViewID, RootID, MountID, ConnectionID string
	MountPath, ExportPath, ExportSource   string
	ServerPort, MountPort                 int
	CachePolicy, RetryPolicy, Timeout     string
	RSize, WSize                          int
	MountOptions                          string
	ParentDev, MountedDev                 int32
	HandshakeUID, HandshakeGID            uint32
	AllowedDataUID, AllowedDataGID        uint32
}

type rootMount struct {
	grant        adapter.RootGrant
	mountPath    string
	server       *nfsserver.Server
	capability   nfsserver.ExportCapability
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
	dirtyRelPaths   map[string]map[string]struct{}
	lastFilesSynced atomic.Int64
	lastDirsSynced  atomic.Int64
	accessCount     atomic.Int64
	releasing       atomic.Bool
	releaseMu       sync.Mutex
	coreReleased    atomic.Bool
	runtimeCleaned  atomic.Bool
}

// Adapter implements adapter.Adapter for macOS built-in NFSv3 loopback worker mounts.
type Adapter struct {
	cfg       Config
	facts     adapter.AdapterFacts
	mu        sync.RWMutex
	views     map[string]*viewState
	preparing map[string]struct{}
}

// New constructs a macOS NFSv3 loopback adapter.
func New(cfg Config) *Adapter {
	if cfg.HostOS == "" {
		cfg.HostOS = runtime.GOOS
	}
	if cfg.MountNFSBin == "" {
		cfg.MountNFSBin = DefaultMountNFSBin
	}
	if cfg.UmountBin == "" {
		cfg.UmountBin = DefaultUmountBin
	}
	if cfg.DiskutilBin == "" {
		cfg.DiskutilBin = DefaultDiskutilBin
	}
	if cfg.MountTimeout <= 0 {
		cfg.MountTimeout = 10 * time.Second
	}
	return &Adapter{
		cfg: cfg,
		facts: adapter.AdapterFacts{
			AdapterID:                 adapter.AdapterNFSDarwin,
			PreparationKind:           adapter.PreparationMounted,
			UntouchedSourceVisibility: adapter.VisibilityLive,
			CacheRevalidation: adapter.CacheRevalidation{
				Mode:                 fmt.Sprintf("%s,rsize=%d,wsize=%d,retrans=%d,timeo=%d,soft", DefaultCachePolicy, DefaultRSize, DefaultWSize, DefaultRetrans, DefaultTimeo),
				ExplicitInvalidation: false,
				AttributeTTL:         0,
			},
			MetadataCapabilities: adapter.MetadataCapabilities{
				ClientMayRemoveEarlySidecar: true,
				SupportsSymlinks:            true, SupportsHardLinks: true, SupportsXattrs: true,
				SupportsChmod: true, SupportsTimestamps: true, SupportsPOSIXLocks: true, SupportsFlock: true,
			},
			SupportsPrivateGit:        true,
			SupportsCheckpointBarrier: true,
		},
		views:     make(map[string]*viewState),
		preparing: make(map[string]struct{}),
	}
}

func (st *viewState) resetFlushStats() {
	st.lastFilesSynced.Store(0)
	st.lastDirsSynced.Store(0)
}

// NoopQuiesceHook is a QuiesceHook for callers that have already stopped worker writers on the host.
func NoopQuiesceHook(_ context.Context, _ string) error { return nil }

// NoWritableMappingsFlushHook is a FlushClientHook for callers that hold no writable client mmap regions.
func NoWritableMappingsFlushHook(_ context.Context, _ string) error { return nil }

// ID returns the canonical adapter identifier (adapter.AdapterNFSDarwin).
func (a *Adapter) ID() adapter.AdapterID { return adapter.AdapterNFSDarwin }

// Name returns the canonical adapter identifier (adapter.AdapterNFSDarwin).
func (a *Adapter) Name() adapter.AdapterID { return adapter.AdapterNFSDarwin }

// Facts returns the static capability and coherence facts for nfsdarwin.
func (a *Adapter) Facts() adapter.AdapterFacts { return a.facts }

// SetConfigCallbacksForTest updates the fault-injection callbacks for client file sync and runtime directory cleanup.
func (a *Adapter) SetConfigCallbacksForTest(clientSyncFn func(string) error, removeAllFn func(string) error) {
	a.mu.Lock()
	a.cfg.ClientSyncFn = clientSyncFn
	a.cfg.RemoveAllFn = removeAllFn
	a.mu.Unlock()
}

// TrackDirtyClientPath registers a relative path under (viewID, rootID) to be synced on the next barrier flush.
func (a *Adapter) TrackDirtyClientPath(viewID, rootID, relPath string) {
	a.mu.RLock()
	st := a.views[viewID]
	a.mu.RUnlock()
	if st == nil || strings.TrimSpace(relPath) == "" {
		return
	}
	if rootID == "" {
		rootID = st.primaryRoot
	}
	st.dirtyMu.Lock()
	if st.dirtyRelPaths == nil {
		st.dirtyRelPaths = make(map[string]map[string]struct{})
	}
	if st.dirtyRelPaths[rootID] == nil {
		st.dirtyRelPaths[rootID] = make(map[string]struct{})
	}
	st.dirtyRelPaths[rootID][filepath.Clean(filepath.FromSlash(relPath))] = struct{}{}
	st.dirtyMu.Unlock()
}

// LastFlushStatsForView returns the counts of regular files and directories synced during the most recent flush on viewID.
func (a *Adapter) LastFlushStatsForView(viewID string) (filesSynced, dirsSynced int) {
	a.mu.RLock()
	st := a.views[viewID]
	a.mu.RUnlock()
	if st == nil {
		return 0, 0
	}
	return int(st.lastFilesSynced.Load()), int(st.lastDirsSynced.Load())
}

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

// ServerForView returns the primary *nfsserver.Server for viewID, or nil if unknown.
func (a *Adapter) ServerForView(viewID string) *nfsserver.Server {
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

// Probe verifies macOS platform support, loopback NFSv3 server binding, /sbin/mount_nfs
// mounting on a user-owned runtime directory, immediate native/shell coherence, and clean unmount.
func (a *Adapter) Probe(ctx context.Context, probeCfg adapter.ProbeConfig) (adapter.ProbeResult, error) {
	res := adapter.ProbeResult{AdapterID: adapter.AdapterNFSDarwin, Attempts: 1}
	if err := ctx.Err(); err != nil {
		res.ReasonCode, res.Diagnostic = adapter.ReasonContextCanceled, err.Error()
		return res, err
	}
	if a.cfg.HostOS != "darwin" {
		res.ReasonCode = adapter.ReasonUnsupportedPlatform
		res.Diagnostic = fmt.Sprintf("nfsdarwin requires darwin host, got %s", a.cfg.HostOS)
		return res, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic}
	}
	if strings.EqualFold(strings.TrimSpace(probeCfg.ConfinementContext), string(adapter.ReasonConfinementUnavailable)) {
		res.ReasonCode = adapter.ReasonConfinementUnavailable
		res.Diagnostic = "required mount confinement context is unavailable"
		return res, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic}
	}
	if a.cfg.ConfinementCheckFn == nil {
		res.ReasonCode = adapter.ReasonConfinementUnavailable
		res.Diagnostic = "no confinement check is configured, so direct backing denial cannot be established"
		return res, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic}
	}
	if cErr := a.cfg.ConfinementCheckFn(ctx, probeCfg); cErr != nil {
		res.ReasonCode, res.Diagnostic = adapter.ReasonConfinementUnavailable, cErr.Error()
		return res, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: cErr}
	}
	res.BackingDenialVerified = true
	if info, err := os.Stat(a.cfg.MountNFSBin); err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		res.ReasonCode = adapter.ReasonHelperMissing
		res.Diagnostic = fmt.Sprintf("mount_nfs binary unavailable at %s: %v", a.cfg.MountNFSBin, err)
		return res, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: err}
	}

	probeBase, err := prepareProbeBaseDir(probeCfg.RuntimeDir)
	if err != nil {
		res.ReasonCode, res.Diagnostic = adapter.ReasonMountDenied, err.Error()
		return res, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: err}
	}
	// Runs after the probe view's release, and refuses to delete through a mount that outlived it.
	defer func() { _ = a.removeRuntimeDir(probeBase) }()

	trunkDir, backingDir, runtimeDir := filepath.Join(probeBase, "trunk"), filepath.Join(probeBase, "backing"), filepath.Join(probeBase, "runtime")
	for _, d := range []string{trunkDir, backingDir, runtimeDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			res.ReasonCode, res.Diagnostic = adapter.ReasonMountDenied, err.Error()
			return res, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: err}
		}
	}
	if err := os.WriteFile(filepath.Join(trunkDir, "probe.txt"), []byte("seed\n"), 0o644); err != nil {
		res.ReasonCode, res.Diagnostic = adapter.ReasonMountDenied, err.Error()
		return res, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: err}
	}

	probeViewID := fmt.Sprintf("probe-nfsdarwin-%d", time.Now().UnixNano())
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
		return res, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: rErr}
	}
	res.ReadExercised = true

	if err := os.WriteFile(probeFile, []byte("coherent-probe\n"), 0o644); err != nil {
		res.ReasonCode, res.Diagnostic = adapter.ReasonProbeReadWriteFailed, err.Error()
		return res, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: err}
	}
	out, exitCode, cErr := execpkg.Run(ctx, "/bin/sh", []string{"-c", "cat probe.txt"}, execpkg.ExecOpts{Launch: execpkg.HostLaunch("nfs_probe"), Dir: prep.PrimaryWorkPath})
	if cErr != nil || exitCode != 0 || strings.TrimSpace(string(out)) != "coherent-probe" {
		res.ReasonCode, res.Diagnostic = adapter.ReasonProbeReadWriteFailed, fmt.Sprintf("shell coherence check failed: exit=%d err=%v out=%q", exitCode, cErr, string(out))
		return res, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: cErr}
	}
	res.WriteExercised = true

	if trunkRaw, tErr := os.ReadFile(filepath.Join(trunkDir, "probe.txt")); tErr != nil || string(trunkRaw) != "seed\n" {
		res.ReasonCode, res.Diagnostic = adapter.ReasonBackingConfinementFailed, "trunk file mutated during probe"
		return res, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic}
	}

	if _, rErr := a.Release(ctx, probeViewID); rErr != nil {
		res.ReasonCode, res.Diagnostic = adapter.ReasonCleanupFailed, rErr.Error()
		return res, rErr
	}
	res.CleanedUp, res.Supported = true, true
	return res, nil
}
