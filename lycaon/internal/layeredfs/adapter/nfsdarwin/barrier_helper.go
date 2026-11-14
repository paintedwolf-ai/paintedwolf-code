//go:build darwin

package nfsdarwin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/flushhelper"
)

const nfsBarrierFlushHelperEnv = "PW_NFS_BARRIER_FLUSH_HELPER"

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
	if os.Getenv(nfsBarrierFlushHelperEnv) == "1" {
		flushhelper.Serve(flushNFSMount)
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

func flushNFSMount(mountRoot string, rels []string) (files, dirs int64, err error) {
	if err := syncVolume(mountRoot); err != nil {
		return 0, 0, fmt.Errorf("sync volume %s: %w", mountRoot, err)
	}
	adp := &Adapter{}
	for _, rel := range rels {
		target := mountRoot
		if rel != "." {
			target = filepath.Join(mountRoot, rel)
		}
		isFile, isDir, err := adp.syncClientPath(context.Background(), target)
		if err != nil {
			return files, dirs, err
		}
		if isFile {
			files++
		} else if isDir {
			dirs++
		}
	}
	return files, dirs, nil
}

func runNFSBarrierFlushSubprocess(ctx context.Context, a *Adapter, st *viewState, mountPath string, rels []string) error {
	exePath, exeErr := resolveHelperExecutable(a)
	if exeErr != nil || strings.TrimSpace(exePath) == "" {
		if exeErr == nil {
			exeErr = errors.New("empty helper executable path")
		}
		return &adapter.AdapterError{
			AdapterID:  adapter.AdapterNFSDarwin,
			Op:         "barrier_flush_helper",
			ReasonCode: adapter.ReasonBarrierFailed,
			Diagnostic: fmt.Sprintf("resolve helper executable for mount barrier flush: %v", exeErr),
			Cause:      exeErr,
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
		Launch:    execpkg.HostLaunch("nfs_barrier_flush"),
		InlineEnv: map[string]string{nfsBarrierFlushHelperEnv: "1"},
		Stdin:     &execpkg.StdinSpec{Literal: flushhelper.Request(mountPath, rels)},
		Started: func(pid int) {
			startedPID = pid
			recordHelperStarted(a, viewID, pid)
		},
	})
	if runErr != nil || exitCode != 0 {
		if cErr := ctx.Err(); cErr != nil {
			return cErr
		}
		if runErr != nil {
			return runErr
		}
		msg := strings.TrimSpace(string(stderr))
		if msg == "" {
			msg = fmt.Sprintf("nfs_barrier_flush helper exited with code %d", exitCode)
		}
		return errors.New(msg)
	}
	fSync, dSync := flushhelper.Reply(stdout)
	st.lastFilesSynced.Add(fSync)
	st.lastDirsSynced.Add(dSync)
	return nil
}
