package nfsdarwin

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

// runBoundedHelper runs a mount helper through the launch door and returns when it exits or ctx ends,
// even if the process cannot be killed: a helper stuck in the kernel on a dead server would otherwise
// hold the caller forever. The stuck process is reported through the returned error, not hidden.
func runBoundedHelper(ctx context.Context, subject, bin string, args ...string) ([]byte, error) {
	type outcome struct {
		out  []byte
		code int
		err  error
	}
	var pid atomic.Int64
	done := make(chan outcome, 1)
	go func() {
		out, code, err := execpkg.Run(ctx, bin, args, execpkg.ExecOpts{
			Launch: execpkg.HostLaunch(subject), NoTimeout: true,
			Started: func(started int) { pid.Store(int64(started)) },
		})
		done <- outcome{out, code, err}
	}()
	finish := func(got outcome) ([]byte, error) {
		if got.err == nil && got.code != 0 {
			got.err = fmt.Errorf("%s exited %d", filepath.Base(bin), got.code)
		}
		return got.out, got.err
	}
	select {
	case got := <-done:
		return finish(got)
	case <-ctx.Done():
	}
	select {
	case got := <-done:
		out, err := finish(got)
		return out, errors.Join(ctx.Err(), err)
	case <-time.After(unmountWaitBound):
		return nil, fmt.Errorf("%s pid %d unsettled after kill: %w", filepath.Base(bin), pid.Load(), ctx.Err())
	}
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
		_, _ = runBoundedHelper(tCtx, "nfs_unmount", umountBin, m.mountPath)
		cancel()
		if !isMountOwnedAndActive(m, false) {
			return nil
		}
	}
	fCtx, fCancel := context.WithTimeout(context.Background(), unmountWaitBound)
	out, fErr := runBoundedHelper(fCtx, "nfs_unmount", umountBin, "-f", m.mountPath)
	fCancel()
	if !isMountOwnedAndActive(m, false) {
		return nil
	}
	dCtx, dCancel := context.WithTimeout(context.Background(), unmountWaitBound)
	dOut, dErr := runBoundedHelper(dCtx, "nfs_unmount", diskutilBin, "unmount", "force", m.mountPath)
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
