package nfsdarwin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

// runBoundedHelper runs a mount helper and returns when it exits or ctx ends, even if the process
// cannot be killed: a helper stuck in the kernel on a dead server would otherwise hold the caller
// forever. The stuck process is reported through the returned error, not hidden.
func runBoundedHelper(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.Command(bin, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return out.Bytes(), err
	case <-ctx.Done():
		_ = cmd.Process.Kill()
	}
	select {
	case err := <-done:
		return out.Bytes(), errors.Join(ctx.Err(), err)
	case <-time.After(unmountWaitBound):
		return nil, fmt.Errorf("%s pid %d unsettled after kill: %w", filepath.Base(bin), cmd.Process.Pid, ctx.Err())
	}
}

// ReclaimDeadMounts force-unmounts this user's view mounts under runtimeRoot that this adapter is
// not serving, left by an engine that died. Identity comes from the mount table's export source and
// owner; a path match alone never qualifies. Holders are not signaled: their I/O fails.
func (a *Adapter) ReclaimDeadMounts(ctx context.Context, runtimeRoot string) ([]string, error) {
	found, err := deadViewMounts(canonicalPath(runtimeRoot), uint32(os.Getuid()))
	if err != nil {
		return nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "reclaim", ReasonCode: adapter.ReasonCleanupFailed, Diagnostic: err.Error(), Cause: err}
	}
	a.mu.RLock()
	umountBin := a.cfg.UmountBin
	live := make(map[string]struct{})
	for _, st := range a.views {
		for _, m := range st.mounts {
			live[m.mountPath] = struct{}{}
		}
	}
	a.mu.RUnlock()
	reclaimed := make([]string, 0, len(found))
	for _, mountPath := range found {
		if _, serving := live[mountPath]; serving {
			continue
		}
		rCtx, cancel := context.WithTimeout(ctx, unmountWaitBound)
		out, uErr := runBoundedHelper(rCtx, umountBin, "-f", mountPath)
		cancel()
		if uErr != nil {
			return reclaimed, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "reclaim", ReasonCode: adapter.ReasonCleanupFailed, Diagnostic: fmt.Sprintf("%s: %v (%s)", mountPath, uErr, strings.TrimSpace(string(out))), Cause: uErr}
		}
		reclaimed = append(reclaimed, mountPath)
	}
	return reclaimed, nil
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
		tCtx, cancel := context.WithTimeout(uCtx, unmountWaitBound)
		_, _ = runBoundedHelper(tCtx, umountBin, m.mountPath)
		cancel()
		if !isMountOwnedAndActive(m, false) {
			return nil
		}
	}
	fCtx, fCancel := context.WithTimeout(context.Background(), unmountWaitBound)
	out, fErr := runBoundedHelper(fCtx, umountBin, "-f", m.mountPath)
	fCancel()
	if !isMountOwnedAndActive(m, false) {
		return nil
	}
	dCtx, dCancel := context.WithTimeout(context.Background(), unmountWaitBound)
	dOut, dErr := runBoundedHelper(dCtx, diskutilBin, "unmount", "force", m.mountPath)
	dCancel()
	if !isMountOwnedAndActive(m, false) {
		return nil
	}
	return &adapter.AdapterError{
		AdapterID: adapter.AdapterNFSDarwin, Op: "release", ReasonCode: adapter.ReasonCleanupFailed,
		Diagnostic: fmt.Sprintf("unmount %s failed: umount=%v (%s) diskutil=%v (%s)", m.mountPath, fErr, strings.TrimSpace(string(out)), dErr, strings.TrimSpace(string(dOut))),
		Cause:      fErr,
	}
}
