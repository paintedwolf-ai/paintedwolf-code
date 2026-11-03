package nfsdarwin

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

// DisownMountForTest mutates the recorded mount ownership metadata for viewID so tests can
// verify that Release and unmountOwnedRoot refuse to unmount an unowned or mismatched path.
func (a *Adapter) DisownMountForTest(viewID, unownedPath string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	st, ok := a.views[viewID]
	if !ok || st == nil {
		return &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "disown", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: viewID}
	}
	m := st.mounts[st.primaryRoot]
	if m == nil {
		return &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "disown", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: viewID}
	}
	m.mountPath = canonicalPath(unownedPath)
	m.facts.MountPath = m.mountPath
	m.facts.ExportSource = "127.0.0.1:/views/unowned/primary/fake"
	return nil
}

// SetUmountBinForTest updates UmountBin and DiskutilBin so tests can inject unmount failures and restore them on retry.
func (a *Adapter) SetUmountBinForTest(umountBin, diskutilBin string) {
	a.mu.Lock()
	if umountBin != "" {
		a.cfg.UmountBin = umountBin
	} else {
		a.cfg.UmountBin = DefaultUmountBin
	}
	if diskutilBin != "" {
		a.cfg.DiskutilBin = diskutilBin
	} else {
		a.cfg.DiskutilBin = DefaultDiskutilBin
	}
	a.mu.Unlock()
}

func verifyMountAndServerForBarrier(st *viewState, viewID, rootID string, m *rootMount, op string) error {
	if err := checkServerAlive(m); err != nil {
		if st.privateGitMgr != nil {
			st.privateGitMgr.RevokeMountedViewBinding(viewID, rootID)
		}
		return &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: op, ReasonCode: adapter.ReasonBarrierFailed, Diagnostic: err.Error(), Cause: err}
	}
	if !isMountOwnedAndActive(m, true) {
		if st.privateGitMgr != nil {
			st.privateGitMgr.RevokeMountedViewBinding(viewID, rootID)
		}
		return &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: op, ReasonCode: adapter.ReasonBarrierFailed, Diagnostic: "mount ownership lost on " + m.mountPath}
	}
	return nil
}

func (a *Adapter) syncClientPath(path string) error {
	a.mu.RLock()
	fn := a.cfg.ClientSyncFn
	a.mu.RUnlock()
	if fn != nil {
		return fn(path)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := f.Sync()
	closeErr := f.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func (a *Adapter) syncMountedRootFiles(ctx context.Context, core *layeredfs.Core, rootID, mountPath string) error {
	seen := make(map[string]bool)
	if core != nil {
		if changes, err := core.EnumerateChanges(false); err == nil {
			for _, ce := range changes {
				if ce.Path.RootID != rootID || ce.Deleted || ce.Kind == layeredfs.EntryAbsent || ce.Kind == layeredfs.EntrySymlink {
					continue
				}
				p := filepath.Join(mountPath, filepath.FromSlash(ce.Path.Rel))
				seen[p] = true
				if err := a.syncClientPath(p); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
		}
	}
	walkErr := filepath.WalkDir(mountPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if cErr := ctx.Err(); cErr != nil {
			return cErr
		}
		if d.Name() == ".git" {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 || seen[path] {
			return nil
		}
		seen[path] = true
		return a.syncClientPath(path)
	})
	if walkErr != nil {
		return walkErr
	}
	if !seen[mountPath] {
		return a.syncClientPath(mountPath)
	}
	return nil
}

func (a *Adapter) removeRuntimeDir(path string) error {
	a.mu.RLock()
	fn := a.cfg.RemoveAllFn
	a.mu.RUnlock()
	if fn != nil {
		return fn(path)
	}
	return os.RemoveAll(path)
}

func checkServerAlive(m *rootMount) error {
	if m == nil || m.server == nil {
		return errors.New("nfsdarwin: nil server")
	}
	if m.serverDead.Load() {
		return errors.New("nfsdarwin: loopback NFS server killed")
	}
	conn, err := net.DialTimeout("tcp", m.server.Addr().String(), 250*time.Millisecond)
	if err != nil {
		m.serverDead.Store(true)
		return fmt.Errorf("nfsdarwin: loopback NFS server unreachable at %s: %w", m.server.Addr(), err)
	}
	_ = conn.Close()
	return nil
}

func isMountOwnedAndActive(m *rootMount, _ bool) bool {
	if m == nil || m.mountPath == "" {
		return false
	}
	found, matchesSource := checkKernelMountTable(m.mountPath, m.facts.ExportSource)
	return found && matchesSource
}

func (a *Adapter) unmountOwnedRoot(ctx context.Context, m *rootMount, serverAlive bool) error {
	if m == nil || m.mountPath == "" || !isMountOwnedAndActive(m, serverAlive) {
		return nil
	}
	a.mu.RLock()
	umountBin, diskutilBin := a.cfg.UmountBin, a.cfg.DiskutilBin
	a.mu.RUnlock()
	uCtx := ctx
	if uCtx == nil || uCtx.Err() != nil {
		uCtx = context.Background()
	}
	if serverAlive {
		tCtx, cancel := context.WithTimeout(uCtx, 3*time.Second)
		err := exec.CommandContext(tCtx, umountBin, m.mountPath).Run()
		cancel()
		if err == nil && !isMountOwnedAndActive(m, false) {
			return nil
		}
	}
	fCtx, fCancel := context.WithTimeout(context.Background(), 5*time.Second)
	out, fErr := exec.CommandContext(fCtx, umountBin, "-f", m.mountPath).CombinedOutput()
	fCancel()
	if fErr == nil && !isMountOwnedAndActive(m, false) {
		return nil
	}
	dCtx, dCancel := context.WithTimeout(context.Background(), 5*time.Second)
	dOut, dErr := exec.CommandContext(dCtx, diskutilBin, "unmount", "force", m.mountPath).CombinedOutput()
	dCancel()
	if dErr == nil && !isMountOwnedAndActive(m, false) {
		return nil
	}
	return &adapter.AdapterError{
		AdapterID: adapter.AdapterNFSDarwin, Op: "release", ReasonCode: adapter.ReasonCleanupFailed,
		Diagnostic: fmt.Sprintf("unmount %s failed: umount=%v (%s) diskutil=%v (%s)", m.mountPath, fErr, strings.TrimSpace(string(out)), dErr, strings.TrimSpace(string(dOut))),
		Cause:      fErr,
	}
}

func normalizeRootGrants(grants []adapter.RootGrant) ([]layeredfs.RootSpec, string, []string, error) {
	coreRoots := make([]layeredfs.RootSpec, 0, len(grants))
	denyPaths := make([]string, 0, len(grants))
	var primaryRoot string
	for i, g := range grants {
		if strings.TrimSpace(g.RootID) == "" || strings.TrimSpace(g.SourcePath) == "" {
			return nil, "", nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "prepare", ReasonCode: adapter.ReasonMountDenied, Diagnostic: "grant RootID and SourcePath are required"}
		}
		absTrunk, err := filepath.Abs(g.SourcePath)
		if err != nil {
			return nil, "", nil, err
		}
		isPrimary := g.IsPrimary || (primaryRoot == "" && i == 0)
		if isPrimary && primaryRoot == "" {
			primaryRoot = g.RootID
		}
		label := g.Label
		if label == "" {
			label = g.RootID
		}
		coreRoots = append(coreRoots, layeredfs.RootSpec{
			ID: g.RootID, Label: label, TrunkPath: absTrunk, Subroot: g.Subroot, IsPrimary: isPrimary,
		})
		denyPaths = append(denyPaths, absTrunk)
	}
	return coreRoots, primaryRoot, denyPaths, nil
}

func prepareProbeBaseDir(runtimeRoot string) (string, func(), error) {
	if strings.TrimSpace(runtimeRoot) != "" {
		if err := os.MkdirAll(runtimeRoot, 0o700); err != nil {
			return "", nil, err
		}
		d, err := os.MkdirTemp(runtimeRoot, "nfsdarwin-probe-*")
		if err != nil {
			return "", nil, err
		}
		return d, func() { _ = os.RemoveAll(d) }, nil
	}
	d, err := os.MkdirTemp("", "nfsdarwin-probe-*")
	if err != nil {
		return "", nil, err
	}
	return d, func() { _ = os.RemoveAll(d) }, nil
}

func classifyProbeError(err error) adapter.ReasonCode {
	var aErr *adapter.AdapterError
	if errors.As(err, &aErr) && aErr.ReasonCode != "" {
		return aErr.ReasonCode
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return adapter.ReasonContextCanceled
	}
	return adapter.ReasonMountDenied
}
