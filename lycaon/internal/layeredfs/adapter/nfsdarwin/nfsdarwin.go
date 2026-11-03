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
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
	// DefaultCachePolicy is the explicit zero-attribute/negative-name cache option set.
	DefaultCachePolicy = "actimeo=0,acregmin=0,acregmax=0,acdirmin=0,acdirmax=0,nonegnamecache"
)

// Config configures the macOS NFSv3 loopback adapter.
type Config struct {
	HostOS        string
	MountNFSBin   string
	UmountBin     string
	DiskutilBin   string
	MountTimeout  time.Duration
	FaultInjector *nfsserver.FaultInjector
}

// MountFacts records the concrete mount options, ports, and export identity for a mounted root.
type MountFacts struct {
	ViewID         string
	RootID         string
	MountID        string
	ConnectionID   string
	MountPath      string
	ExportPath     string
	ExportSource   string
	ServerPort     int
	MountPort      int
	CachePolicy    string
	RetryPolicy    string
	Timeout        string
	RSize          int
	WSize          int
	MountOptions   string
	ParentDev      int32
	MountedDev     int32
	HandshakeUID   uint32
	HandshakeGID   uint32
	AllowedDataUID uint32
	AllowedDataGID uint32
}

type rootMount struct {
	grant      adapter.RootGrant
	mountPath  string
	server     *nfsserver.Server
	capability nfsserver.ExportCapability
	facts      MountFacts
	serverDead atomic.Bool
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
	accessCount     atomic.Int64
}

// Adapter implements adapter.Adapter for macOS built-in NFSv3 loopback worker mounts.
type Adapter struct {
	cfg   Config
	facts adapter.AdapterFacts
	mu    sync.RWMutex
	views map[string]*viewState
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
				Mode:                 DefaultCachePolicy + ",rsize=65536,wsize=65536,retrans=2,timeo=10,soft",
				ExplicitInvalidation: false,
				AttributeTTL:         0,
			},
			MetadataCapabilities: adapter.MetadataCapabilities{
				SupportsSymlinks:   true,
				SupportsHardLinks:  true,
				SupportsXattrs:     true,
				SupportsChmod:      true,
				SupportsTimestamps: true,
				SupportsPOSIXLocks: false,
				SupportsFlock:      true,
			},
			SupportsPrivateGit:        true,
			SupportsCheckpointBarrier: true,
		},
		views: make(map[string]*viewState),
	}
}

// ID returns the canonical adapter identifier (adapter.AdapterNFSDarwin).
func (a *Adapter) ID() adapter.AdapterID { return adapter.AdapterNFSDarwin }

// Name returns the canonical adapter identifier (adapter.AdapterNFSDarwin).
func (a *Adapter) Name() adapter.AdapterID { return adapter.AdapterNFSDarwin }

// Facts returns the static capability and coherence facts for nfsdarwin.
func (a *Adapter) Facts() adapter.AdapterFacts { return a.facts }

// MountFactsForView returns the recorded MountFacts for the primary root of viewID.
func (a *Adapter) MountFactsForView(viewID string) (MountFacts, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	st, ok := a.views[viewID]
	if !ok {
		return MountFacts{}, false
	}
	m, ok := st.mounts[st.primaryRoot]
	if !ok || m == nil {
		return MountFacts{}, false
	}
	return m.facts, true
}

// ServerForView returns the primary *nfsserver.Server for viewID, or nil if unknown.
func (a *Adapter) ServerForView(viewID string) *nfsserver.Server {
	a.mu.RLock()
	defer a.mu.RUnlock()
	st, ok := a.views[viewID]
	if !ok {
		return nil
	}
	if m := st.mounts[st.primaryRoot]; m != nil {
		return m.server
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
		res.ReasonCode = adapter.ReasonContextCanceled
		res.Diagnostic = err.Error()
		return res, err
	}
	if a.cfg.HostOS != "darwin" {
		res.ReasonCode = adapter.ReasonUnsupportedPlatform
		res.Diagnostic = fmt.Sprintf("nfsdarwin requires darwin host, got %s", a.cfg.HostOS)
		return res, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic}
	}
	if info, err := os.Stat(a.cfg.MountNFSBin); err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		res.ReasonCode = adapter.ReasonHelperMissing
		res.Diagnostic = fmt.Sprintf("mount_nfs binary unavailable at %s: %v", a.cfg.MountNFSBin, err)
		return res, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: err}
	}

	probeBase, cleanupBase, err := prepareProbeBaseDir(probeCfg.RuntimeDir)
	if err != nil {
		res.ReasonCode = adapter.ReasonMountDenied
		res.Diagnostic = err.Error()
		return res, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: err}
	}
	defer cleanupBase()

	trunkDir := filepath.Join(probeBase, "trunk")
	backingDir := filepath.Join(probeBase, "backing")
	runtimeDir := filepath.Join(probeBase, "runtime")
	for _, d := range []string{trunkDir, backingDir, runtimeDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			res.ReasonCode = adapter.ReasonMountDenied
			res.Diagnostic = err.Error()
			return res, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: err}
		}
	}
	if err := os.WriteFile(filepath.Join(trunkDir, "probe.txt"), []byte("seed\n"), 0o644); err != nil {
		res.ReasonCode = adapter.ReasonMountDenied
		res.Diagnostic = err.Error()
		return res, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: err}
	}

	probeViewID := fmt.Sprintf("probe-nfsdarwin-%d", time.Now().UnixNano())
	prep, pErr := a.Prepare(ctx, adapter.PrepareRequest{
		ViewID:     probeViewID,
		BackingDir: backingDir,
		RuntimeDir: runtimeDir,
		Grants:     []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
	})
	if pErr != nil {
		res.ReasonCode = classifyProbeError(pErr)
		res.Diagnostic = pErr.Error()
		return res, pErr
	}
	res.MountExercised = true
	defer func() { _, _ = a.Release(context.Background(), probeViewID) }()

	probeFile := filepath.Join(prep.PrimaryWorkPath, "probe.txt")
	if raw, rErr := os.ReadFile(probeFile); rErr != nil || string(raw) != "seed\n" {
		res.ReasonCode = adapter.ReasonProbeReadWriteFailed
		res.Diagnostic = fmt.Sprintf("probe read failed: %v", rErr)
		return res, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: rErr}
	}
	res.ReadExercised = true

	if err := os.WriteFile(probeFile, []byte("coherent-probe\n"), 0o644); err != nil {
		res.ReasonCode = adapter.ReasonProbeReadWriteFailed
		res.Diagnostic = err.Error()
		return res, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: err}
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "cat probe.txt")
	cmd.Dir = prep.PrimaryWorkPath
	out, cErr := cmd.CombinedOutput()
	if cErr != nil || strings.TrimSpace(string(out)) != "coherent-probe" {
		res.ReasonCode = adapter.ReasonProbeReadWriteFailed
		res.Diagnostic = fmt.Sprintf("shell coherence check failed: err=%v out=%q", cErr, string(out))
		return res, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: cErr}
	}
	res.WriteExercised = true

	trunkRaw, tErr := os.ReadFile(filepath.Join(trunkDir, "probe.txt"))
	if tErr != nil || string(trunkRaw) != "seed\n" {
		res.ReasonCode = adapter.ReasonBackingConfinementFailed
		res.Diagnostic = "trunk file mutated during probe"
		return res, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic}
	}
	res.BackingDenialVerified = true

	if _, rErr := a.Release(ctx, probeViewID); rErr != nil {
		res.ReasonCode = adapter.ReasonCleanupFailed
		res.Diagnostic = rErr.Error()
		return res, rErr
	}
	res.CleanedUp = true
	res.Supported = true
	return res, nil
}
