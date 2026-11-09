package nfsdarwin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/fssync"
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

func classifyBarrierReason(err error) adapter.ReasonCode {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return adapter.ReasonContextCanceled
	}
	return adapter.ReasonBarrierFailed
}

// syncClientPath fsyncs one changed path to surface a write error the client recorded against it.
// A file the worker made unreadable cannot be opened here; syncMountedRootFiles has already pushed
// its dirty data by syncing the volume, so that is not a failed barrier.
func (a *Adapter) syncClientPath(ctx context.Context, path string) (isFile, isDir bool, err error) {
	if err := ctx.Err(); err != nil {
		return false, false, err
	}
	a.mu.RLock()
	fn := a.cfg.ClientSyncFn
	a.mu.RUnlock()
	if fn != nil {
		if err := fn(path); err != nil {
			return false, false, err
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, false, nil
		}
		return false, false, err
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return false, false, nil
	}
	// O_NONBLOCK so a file replaced by a FIFO after the Lstat cannot block the barrier in open.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
			return false, false, nil
		}
		return false, false, err
	}
	opened, sErr := f.Stat()
	if sErr != nil || (!opened.Mode().IsRegular() && !opened.IsDir()) {
		_ = f.Close()
		return false, false, sErr
	}
	var mapErr error
	if opened.Mode().IsRegular() {
		mapErr = flushMappedPages(f, opened.Size())
	}
	syncErr := errors.Join(mapErr, fssync.Always(f))
	closeErr := f.Close()
	if syncErr != nil {
		return false, false, syncErr
	}
	if closeErr != nil {
		return false, false, closeErr
	}
	return !info.IsDir(), info.IsDir(), nil
}

func addCandidateWithAncestors(candidates map[string]struct{}, rel string) {
	for cur := filepath.Clean(filepath.FromSlash(rel)); cur != "" && cur != "/" && cur != ".git"; cur = filepath.Dir(cur) {
		if strings.HasPrefix(cur, ".git/") || strings.Contains(cur, "/.git/") || strings.HasSuffix(cur, "/.git") {
			break
		}
		candidates[cur] = struct{}{}
		if cur == "." {
			break
		}
	}
}

func (a *Adapter) syncMountedRootFiles(ctx context.Context, st *viewState, rootID string, m *rootMount) error {
	candidates := map[string]struct{}{".": {}}
	if st.core != nil {
		changes, err := st.core.EnumerateChanges(false)
		if err != nil {
			return err
		}
		for _, ce := range changes {
			if ce.Path.RootID != rootID || ce.Kind == layeredfs.EntrySymlink {
				continue
			}
			if ce.Deleted || ce.Kind == layeredfs.EntryAbsent {
				addCandidateWithAncestors(candidates, filepath.Dir(filepath.FromSlash(ce.Path.Rel)))
			} else {
				addCandidateWithAncestors(candidates, ce.Path.Rel)
			}
		}
	}
	st.dirtyMu.Lock()
	for rel := range st.dirtyRelPaths[rootID] {
		addCandidateWithAncestors(candidates, rel)
	}
	st.dirtyMu.Unlock()

	rels := make([]string, 0, len(candidates))
	for rel := range candidates {
		if rel != "." {
			rels = append(rels, rel)
		}
	}
	sort.Strings(rels)
	rels = append(rels, ".")

	a.mu.RLock()
	fn := a.cfg.ClientSyncFn
	a.mu.RUnlock()
	if fn == nil {
		return runNFSBarrierFlushSubprocess(ctx, a, st, m.mountPath, rels)
	}

	// The volume sync reaches dirty buffers of files the server has never heard of; the per-path
	// fsyncs that follow report write errors, which the volume sync does not return.
	if err := syncVolume(m.mountPath); err != nil {
		return fmt.Errorf("sync volume %s: %w", m.mountPath, err)
	}

	var filesSynced, dirsSynced int64
	for _, rel := range rels {
		target := m.mountPath
		if rel != "." {
			target = filepath.Join(m.mountPath, rel)
		}
		isFile, isDir, err := a.syncClientPath(ctx, target)
		if err != nil {
			return err
		}
		if isFile {
			filesSynced++
		} else if isDir {
			dirsSynced++
		}
	}
	st.lastFilesSynced.Add(filesSynced)
	st.lastDirsSynced.Add(dirsSynced)
	return nil
}

// removeRuntimeDir refuses while anything is mounted below path: recursive removal would otherwise
// delete another filesystem's contents through the mount.
func (a *Adapter) removeRuntimeDir(path string) error {
	remaining, err := mountsWithin(path)
	if err != nil {
		return err
	}
	if len(remaining) > 0 {
		return fmt.Errorf("mounts remain below runtime dir: %s", strings.Join(remaining, ", "))
	}
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
	if m.serverDead.Load() || !m.server.IsAlive() {
		m.serverDead.Store(true)
		return errors.New("nfsdarwin: loopback NFS server stopped")
	}
	return nil
}

func isMountOwnedAndActive(m *rootMount, _ bool) bool {
	if m == nil || m.mountPath == "" {
		return false
	}
	found, matchesSource := checkKernelMountTable(m.mountPath, m.facts.ExportSource)
	return found && matchesSource
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

func prepareProbeBaseDir(runtimeRoot string) (string, error) {
	if strings.TrimSpace(runtimeRoot) != "" {
		if err := os.MkdirAll(runtimeRoot, 0o700); err != nil {
			return "", err
		}
	}
	return os.MkdirTemp(runtimeRoot, "nfsdarwin-probe-*")
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
