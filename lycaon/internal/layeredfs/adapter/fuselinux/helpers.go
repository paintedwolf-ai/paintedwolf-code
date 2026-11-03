package fuselinux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
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
		return &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "disown", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: viewID}
	}
	m := st.mounts[st.primaryRoot]
	if m == nil {
		return &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "disown", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: viewID}
	}
	m.mountPath = canonicalPath(unownedPath)
	m.facts.MountPath = m.mountPath
	m.facts.FSName = "lycaon-unowned-fake"
	return nil
}

// SetFusermountBinForTest updates FusermountBin so tests can inject unmount failures and restore them on retry.
func (a *Adapter) SetFusermountBinForTest(fusermountBin string) {
	a.mu.Lock()
	if fusermountBin != "" {
		a.cfg.FusermountBin = fusermountBin
	} else if resolved, err := exec.LookPath("fusermount3"); err == nil && resolved != "" {
		a.cfg.FusermountBin = resolved
	} else {
		a.cfg.FusermountBin = DefaultFusermountBin
	}
	a.mu.Unlock()
}

// InvalidatePath sends an explicit FUSE kernel cache invalidation for qpath on viewID.
func (a *Adapter) InvalidatePath(viewID string, qpath layeredfs.QualifiedPath) error {
	a.mu.RLock()
	st, ok := a.views[viewID]
	a.mu.RUnlock()
	if !ok || st == nil {
		return &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "invalidate_path", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: viewID}
	}
	rootID := qpath.RootID
	if rootID == "" {
		rootID = st.primaryRoot
	}
	m := st.mounts[rootID]
	if m == nil || m.server == nil {
		return &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "invalidate_path", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: rootID}
	}
	m.server.InvalidateRelPath(qpath.Rel)
	return nil
}

func verifyMountAndServerForBarrier(st *viewState, viewID, rootID string, m *rootMount, op string) error {
	if err := checkServerAlive(m); err != nil {
		if st.privateGitMgr != nil {
			st.privateGitMgr.RevokeMountedViewBinding(viewID, rootID)
		}
		return &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: op, ReasonCode: adapter.ReasonBarrierFailed, Diagnostic: err.Error(), Cause: err}
	}
	if !isMountOwnedAndActive(m, true) {
		if st.privateGitMgr != nil {
			st.privateGitMgr.RevokeMountedViewBinding(viewID, rootID)
		}
		return &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: op, ReasonCode: adapter.ReasonBarrierFailed, Diagnostic: "mount ownership lost on " + m.mountPath}
	}
	return nil
}

func classifyBarrierError(err error) adapter.ReasonCode {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return adapter.ReasonContextCanceled
	}
	return adapter.ReasonBarrierFailed
}

// TrackDirtyClientPath registers a relative path under (viewID, rootID) in the bounded client flush set.
func (a *Adapter) TrackDirtyClientPath(viewID, rootID, relPath string) {
	a.mu.RLock()
	st := a.views[viewID]
	a.mu.RUnlock()
	if st == nil {
		return
	}
	if rootID == "" {
		rootID = st.primaryRoot
	}
	clean := cleanFuseRel(relPath)
	st.dirtyMu.Lock()
	if st.dirtyPaths[rootID] == nil {
		st.dirtyPaths[rootID] = make(map[string]struct{})
	}
	st.dirtyPaths[rootID][clean] = struct{}{}
	st.dirtyMu.Unlock()
}

// LastFlushStatsForView returns the bounded count of regular files and directories synced
// during the most recent FlushClientData or CheckpointBarrier for viewID.
func (a *Adapter) LastFlushStatsForView(viewID string) (filesSynced, dirsSynced int) {
	a.mu.RLock()
	st := a.views[viewID]
	a.mu.RUnlock()
	if st == nil {
		return 0, 0
	}
	st.dirtyMu.Lock()
	defer st.dirtyMu.Unlock()
	return st.lastFilesSynced, st.lastDirsSynced
}

func (a *Adapter) syncClientPath(ctx context.Context, targetPath string) (isDir bool, synced bool, err error) {
	if cErr := ctx.Err(); cErr != nil {
		return false, false, cErr
	}
	info, err := os.Lstat(targetPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, false, nil
		}
		return false, false, err
	}
	mode := info.Mode()
	if mode&(os.ModeSymlink|os.ModeNamedPipe|os.ModeSocket|os.ModeDevice|os.ModeCharDevice) != 0 {
		return false, false, nil
	}
	if !mode.IsRegular() && !mode.IsDir() {
		return false, false, nil
	}
	a.mu.RLock()
	fn := a.cfg.ClientSyncFn
	a.mu.RUnlock()
	if fn != nil {
		if fnErr := fn(targetPath); fnErr != nil {
			return mode.IsDir(), false, fnErr
		}
	}
	f, err := os.Open(targetPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, false, nil
		}
		return mode.IsDir(), false, err
	}
	syncErr := f.Sync()
	closeErr := f.Close()
	if syncErr != nil {
		return mode.IsDir(), false, syncErr
	}
	if closeErr != nil {
		return mode.IsDir(), false, closeErr
	}
	return mode.IsDir(), true, nil
}

func collectBoundedFlushPaths(st *viewState, rootID string, m *rootMount) []string {
	relSet := map[string]struct{}{".": {}}
	addRel := func(rel string) {
		cur := cleanFuseRel(rel)
		for cur != "" && cur != "." {
			relSet[cur] = struct{}{}
			parent := path.Dir(cur)
			if parent == cur {
				break
			}
			cur = cleanFuseRel(parent)
		}
		relSet["."] = struct{}{}
	}
	if st.core != nil {
		if changes, err := st.core.EnumerateChanges(false); err == nil {
			for _, ce := range changes {
				if ce.Path.RootID != rootID {
					continue
				}
				if ce.Deleted || ce.Kind == layeredfs.EntryAbsent {
					addRel(path.Dir(cleanFuseRel(ce.Path.Rel)))
					continue
				}
				if ce.Kind != layeredfs.EntrySymlink {
					addRel(ce.Path.Rel)
				}
			}
		}
	}
	if m.server != nil {
		for _, r := range m.server.DirtyCandidatePaths() {
			addRel(r)
		}
	}
	st.dirtyMu.Lock()
	for r := range st.dirtyPaths[rootID] {
		addRel(r)
	}
	st.dirtyMu.Unlock()

	rels := make([]string, 0, len(relSet))
	for r := range relSet {
		if r != "." {
			rels = append(rels, r)
		}
	}
	sort.Strings(rels)
	out := make([]string, 0, len(rels)+1)
	for _, r := range rels {
		out = append(out, filepath.Join(m.mountPath, filepath.FromSlash(r)))
	}
	out = append(out, m.mountPath)
	return out
}

func (a *Adapter) syncMountedRootFiles(ctx context.Context, st *viewState, rootID string, m *rootMount) (filesSynced, dirsSynced int, err error) {
	for _, p := range collectBoundedFlushPaths(st, rootID, m) {
		isDir, synced, sErr := a.syncClientPath(ctx, p)
		if sErr != nil {
			return filesSynced, dirsSynced, sErr
		}
		if synced {
			if isDir {
				dirsSynced++
			} else {
				filesSynced++
			}
		}
	}
	return filesSynced, dirsSynced, nil
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
		return errors.New("fuselinux: nil server")
	}
	if m.serverDead.Load() || !m.server.IsAlive() {
		m.serverDead.Store(true)
		return errors.New("fuselinux: FUSE server stopped or killed")
	}
	return nil
}

func isMountOwnedAndActive(m *rootMount, _ bool) bool {
	if m == nil || m.mountPath == "" {
		return false
	}
	found, matchesSource := checkKernelMountTable(m.mountPath, m.facts.FSName)
	return found && matchesSource
}

func runFusermountUnmount(ctx context.Context, fusermountBin, mountPath string, lazy bool) error {
	args := []string{"-u"}
	if lazy {
		args = append(args, "-z")
	}
	args = append(args, "--", mountPath)
	out, err := exec.CommandContext(ctx, fusermountBin, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("fusermount3 %v: %w (%s)", args, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (a *Adapter) unmountOwnedRoot(ctx context.Context, m *rootMount, serverAlive bool) error {
	if m == nil || m.mountPath == "" || !isMountOwnedAndActive(m, serverAlive) {
		return nil
	}
	a.mu.RLock()
	fusermountBin := a.cfg.FusermountBin
	a.mu.RUnlock()
	uCtx := ctx
	if uCtx == nil || uCtx.Err() != nil {
		uCtx = context.Background()
	}
	var firstErr error
	if serverAlive {
		tCtx, cancel := context.WithTimeout(uCtx, 3*time.Second)
		firstErr = runFusermountUnmount(tCtx, fusermountBin, m.mountPath, false)
		cancel()
		if !isMountOwnedAndActive(m, false) {
			return nil
		}
	}
	fCtx, fCancel := context.WithTimeout(context.Background(), 5*time.Second)
	lazyErr := runFusermountUnmount(fCtx, fusermountBin, m.mountPath, true)
	fCancel()
	if !isMountOwnedAndActive(m, false) {
		return nil
	}
	return &adapter.AdapterError{
		AdapterID:  adapter.AdapterFUSELinux,
		Op:         "release",
		ReasonCode: adapter.ReasonCleanupFailed,
		Diagnostic: fmt.Sprintf("unmount %s failed: standard=%v lazy=%v", m.mountPath, firstErr, lazyErr),
		Cause:      errors.Join(firstErr, lazyErr),
	}
}
