//go:build linux

package fuselinux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/flushhelper"
)

const fuseBarrierFlushHelperEnv = "PW_FUSE_BARRIER_FLUSH_HELPER"

type helperViewTracker struct {
	lastPID    int
	activePIDs map[int]struct{}
}

var (
	helperRegMu     sync.Mutex
	execPathHooks   = make(map[*Adapter]func() (string, error))
	helperTrackers  = make(map[*Adapter]map[string]*helperViewTracker)
	helperStartHook = make(map[*Adapter]func(viewID string, pid int))
)

func init() {
	if os.Getenv(fuseBarrierFlushHelperEnv) == "1" {
		flushhelper.Serve(flushFuseMount)
	}
}

func recordHelperStarted(a *Adapter, viewID string, pid int) {
	helperRegMu.Lock()
	if helperTrackers[a] == nil {
		helperTrackers[a] = make(map[string]*helperViewTracker)
	}
	tr := helperTrackers[a][viewID]
	if tr == nil {
		tr = &helperViewTracker{activePIDs: make(map[int]struct{})}
		helperTrackers[a][viewID] = tr
	}
	tr.lastPID = pid
	tr.activePIDs[pid] = struct{}{}
	hook := helperStartHook[a]
	helperRegMu.Unlock()
	if hook != nil {
		hook(viewID, pid)
	}
}

func recordHelperFinished(a *Adapter, viewID string, pid int) {
	helperRegMu.Lock()
	if byView := helperTrackers[a]; byView != nil {
		if tr := byView[viewID]; tr != nil {
			delete(tr.activePIDs, pid)
		}
	}
	helperRegMu.Unlock()
}

func resolveHelperExecutable(a *Adapter) (string, error) {
	helperRegMu.Lock()
	fn := execPathHooks[a]
	helperRegMu.Unlock()
	if fn != nil {
		return fn()
	}
	return os.Executable()
}

func collectBoundedFlushPaths(st *viewState, rootID string, m *rootMount) ([]string, error) {
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
		changes, err := st.core.EnumerateChanges(false)
		if err != nil {
			return nil, err
		}
		for _, ce := range changes {
			if ce.Path.RootID != rootID {
				continue
			}
			if ce.Deleted || ce.Kind == layeredfs.EntryAbsent {
				addRel(path.Dir(cleanFuseRel(ce.Path.Rel)))
			} else if ce.Kind != layeredfs.EntrySymlink {
				addRel(ce.Path.Rel)
			}
		}
	}
	if m.server != nil {
		for _, r := range m.server.nodes.dirtyCandidatePaths() {
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
	return append(out, m.mountPath), nil
}

func flushFuseMount(mountRoot string, paths []string) (files, dirs int64, err error) {
	adp := &Adapter{}
	for _, p := range paths {
		isDir, synced, err := adp.syncClientPath(context.Background(), mountRoot, p)
		if err != nil {
			return files, dirs, err
		}
		switch {
		case !synced:
		case isDir:
			dirs++
		default:
			files++
		}
	}
	return files, dirs, nil
}

func runFuseBarrierFlushSubprocess(ctx context.Context, a *Adapter, st *viewState, mountPath string, paths []string) (filesSynced, dirsSynced int, err error) {
	exePath, exeErr := resolveHelperExecutable(a)
	if exeErr != nil || strings.TrimSpace(exePath) == "" {
		if exeErr == nil {
			exeErr = errors.New("empty helper executable path")
		}
		return 0, 0, &adapter.AdapterError{
			AdapterID: adapter.AdapterFUSELinux, Op: "barrier_flush_helper", ReasonCode: adapter.ReasonBarrierFailed,
			Diagnostic: fmt.Sprintf("resolve helper executable for mount barrier flush: %v", exeErr), Cause: exeErr,
		}
	}
	viewID := ""
	if st != nil && st.view != nil {
		viewID = st.view.ViewID
	}
	var startedPID int
	defer func() {
		if startedPID > 0 {
			recordHelperFinished(a, viewID, startedPID)
		}
	}()
	stdout, stderr, exitCode, runErr := execpkg.RunSeparate(ctx, exePath, nil, execpkg.ExecOpts{
		Launch: execpkg.HostLaunch("fuse_barrier_flush"), InlineEnv: map[string]string{fuseBarrierFlushHelperEnv: "1"},
		Stdin: &execpkg.StdinSpec{Literal: flushhelper.Request(mountPath, paths)},
		Started: func(pid int) {
			startedPID = pid
			recordHelperStarted(a, viewID, pid)
		},
	})
	if runErr != nil || exitCode != 0 {
		// The launcher ends a canceled helper, so its exit says only that it was stopped.
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}
		if runErr != nil {
			return 0, 0, runErr
		}
		msg := strings.TrimSpace(string(stderr))
		if msg == "" {
			msg = fmt.Sprintf("fuse_barrier_flush helper exited with code %d", exitCode)
		}
		return 0, 0, errors.New(msg)
	}
	files, dirs := flushhelper.Reply(stdout)
	return int(files), int(dirs), nil
}
