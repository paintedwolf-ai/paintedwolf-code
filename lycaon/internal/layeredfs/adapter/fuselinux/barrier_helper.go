package fuselinux

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

const (
	fuseBarrierFlushHelperEnv    = "PW_FUSE_BARRIER_FLUSH_HELPER"
	fuseBarrierFlushPauseFileEnv = "PW_BARRIER_FLUSH_HELPER_PAUSE_FILE"
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
	if os.Getenv(fuseBarrierFlushHelperEnv) == "1" {
		runFuseBarrierFlushHelperMain()
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

func runFuseBarrierFlushHelperMain() {
	if pauseFile := os.Getenv(fuseBarrierFlushPauseFileEnv); pauseFile != "" {
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
	var filesSynced, dirsSynced int
	adp := &Adapter{}
	for _, p := range parts[1:] {
		if p == "" {
			continue
		}
		isDir, synced, sErr := adp.syncClientPath(context.Background(), mountRoot, p)
		if sErr != nil {
			_, _ = fmt.Fprintln(os.Stderr, sErr.Error())
			os.Exit(1)
		}
		if synced {
			if isDir {
				dirsSynced++
			} else {
				filesSynced++
			}
		}
	}
	_, _ = fmt.Fprintf(os.Stdout, "%d %d\n", filesSynced, dirsSynced)
	os.Exit(0)
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
	payload := strings.Join(append([]string{mountPath}, paths...), "\x00")
	var startedPID int
	defer func() {
		if startedPID > 0 {
			recordHelperFinished(a, viewID, startedPID)
		}
	}()
	stdout, stderr, exitCode, runErr := execpkg.RunSeparate(ctx, exePath, nil, execpkg.ExecOpts{
		Launch: execpkg.HostLaunch("fuse_barrier_flush"), InlineEnv: map[string]string{fuseBarrierFlushHelperEnv: "1"},
		Stdin: &execpkg.StdinSpec{Literal: []byte(payload)},
		Started: func(pid int) {
			startedPID = pid
			recordHelperStarted(a, viewID, pid)
		},
	})
	if runErr != nil || exitCode != 0 {
		if runErr != nil {
			return 0, 0, runErr
		}
		msg := strings.TrimSpace(string(stderr))
		if msg == "" {
			msg = fmt.Sprintf("fuse_barrier_flush helper exited with code %d", exitCode)
		}
		return 0, 0, errors.New(msg)
	}
	_, _ = fmt.Sscanf(strings.TrimSpace(string(stdout)), "%d %d", &filesSynced, &dirsSynced)
	return filesSynced, dirsSynced, nil
}
