package nfsdarwin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

const (
	nfsBarrierFlushHelperEnv    = "PW_NFS_BARRIER_FLUSH_HELPER"
	nfsBarrierFlushPauseFileEnv = "PW_BARRIER_FLUSH_HELPER_PAUSE_FILE"
)

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
		runNFSBarrierFlushHelperMain()
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

func runNFSBarrierFlushHelperMain() {
	if pauseFile := os.Getenv(nfsBarrierFlushPauseFileEnv); pauseFile != "" {
		_ = os.WriteFile(pauseFile, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600)
		for {
			if _, err := os.Stat(pauseFile); errors.Is(err, os.ErrNotExist) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) < 1 || parts[0] == "" {
		os.Exit(2)
	}
	mountRoot := parts[0]
	if err := syncVolume(mountRoot); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "sync volume %s: %v\n", mountRoot, err)
		os.Exit(1)
	}
	var filesSynced, dirsSynced int64
	adp := &Adapter{}
	for _, rel := range parts[1:] {
		if rel == "" {
			continue
		}
		target := mountRoot
		if rel != "." {
			target = filepath.Join(mountRoot, rel)
		}
		isFile, isDir, sErr := adp.syncClientPath(context.Background(), target)
		if sErr != nil {
			_, _ = fmt.Fprintln(os.Stderr, sErr.Error())
			os.Exit(1)
		}
		if isFile {
			filesSynced++
		} else if isDir {
			dirsSynced++
		}
	}
	_, _ = fmt.Fprintf(os.Stdout, "%d %d\n", filesSynced, dirsSynced)
	os.Exit(0)
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
	payload := strings.Join(append([]string{mountPath}, rels...), "\x00")
	var startedPID int
	defer func() {
		if startedPID > 0 {
			recordHelperFinished(a, viewID, startedPID)
		}
	}()
	stdout, stderr, exitCode, runErr := execpkg.RunSeparate(ctx, exePath, nil, execpkg.ExecOpts{
		Launch:    execpkg.HostLaunch("nfs_barrier_flush"),
		InlineEnv: map[string]string{nfsBarrierFlushHelperEnv: "1"},
		Stdin:     &execpkg.StdinSpec{Literal: []byte(payload)},
		Started: func(pid int) {
			startedPID = pid
			recordHelperStarted(a, viewID, pid)
		},
	})
	if runErr != nil || exitCode != 0 {
		if runErr != nil {
			return runErr
		}
		msg := strings.TrimSpace(string(stderr))
		if msg == "" {
			msg = fmt.Sprintf("nfs_barrier_flush helper exited with code %d", exitCode)
		}
		return errors.New(msg)
	}
	var fSync, dSync int64
	_, _ = fmt.Sscanf(strings.TrimSpace(string(stdout)), "%d %d", &fSync, &dSync)
	st.lastFilesSynced.Add(fSync)
	st.lastDirsSynced.Add(dSync)
	return nil
}
