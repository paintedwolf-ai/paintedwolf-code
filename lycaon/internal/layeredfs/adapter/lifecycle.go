package adapter

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

func initViewDirs(viewID, runtimeDir, backingDir string) (string, string, error) {
	if viewID == "" {
		return "", "", fmt.Errorf("ViewID is required")
	}
	if runtimeDir == "" {
		return "", "", fmt.Errorf("RuntimeDir is required")
	}
	viewDir := filepath.Join(runtimeDir, "views", viewID)
	if err := os.MkdirAll(viewDir, 0o700); err != nil {
		return "", "", err
	}
	if backingDir == "" {
		backingDir = filepath.Join(viewDir, "backing")
	}
	if err := os.MkdirAll(backingDir, 0o700); err != nil {
		return "", "", err
	}
	return viewDir, backingDir, nil
}

func exerciseProbeReadWrite(srcDir, backingDir string, res *ProbeResult) error {
	seedFile := filepath.Join(srcDir, "probe.txt")
	if err := os.WriteFile(seedFile, []byte("probe-seed"), 0o600); err != nil {
		res.ReasonCode = ReasonProbeReadWriteFailed
		res.Diagnostic = err.Error()
		return err
	}
	core, err := layeredfs.New(layeredfs.Config{
		ViewID:     "probe-view",
		BackingDir: backingDir,
		Roots:      []layeredfs.RootSpec{{ID: "probe", TrunkPath: srcDir, IsPrimary: true}},
	})
	if err != nil {
		res.ReasonCode = ReasonProbeReadWriteFailed
		res.Diagnostic = err.Error()
		return err
	}
	defer func() { _ = core.ReleaseView() }()

	qp := layeredfs.QualifiedPath{RootID: "probe", Rel: "probe.txt"}
	got, err := core.ReadFile(qp)
	if err != nil || !bytes.Equal(got, []byte("probe-seed")) {
		res.ReasonCode = ReasonProbeReadWriteFailed
		res.Diagnostic = "probe read mismatch"
		return fmt.Errorf("probe read mismatch: %w", err)
	}
	res.ReadExercised = true

	if _, err := core.WriteFile(qp, []byte("probe-mutated"), 0o600); err != nil {
		res.ReasonCode = ReasonProbeReadWriteFailed
		res.Diagnostic = err.Error()
		return err
	}
	res.WriteExercised = true
	return nil
}

func combineFlushHooks(h1, h2 func(ctx context.Context, viewID string) error) func(ctx context.Context, viewID string) error {
	return func(ctx context.Context, viewID string) error {
		if h1 != nil {
			if err := h1(ctx, viewID); err != nil {
				return err
			}
		}
		if h2 != nil {
			return h2(ctx, viewID)
		}
		return nil
	}
}

func executeCheckpointBarrier(
	ctx context.Context,
	adapterID AdapterID,
	core *layeredfs.Core,
	quiesceHook func(ctx context.Context, viewID string) error,
	flushHook func(ctx context.Context, viewID string) error,
) (BarrierReceipt, func() error, error) {
	viewID := core.ViewID()
	if quiesceHook != nil {
		if err := quiesceHook(ctx, viewID); err != nil {
			return BarrierReceipt{}, nil, &AdapterError{
				AdapterID:  adapterID,
				Op:         "checkpoint_barrier",
				ReasonCode: ReasonBarrierFailed,
				Diagnostic: fmt.Sprintf("quiesce writers failed: %v", err),
				Cause:      err,
			}
		}
	}
	token, err := core.BeginCheckpointBarrier(ctx)
	if err != nil {
		return BarrierReceipt{}, nil, &AdapterError{
			AdapterID:  adapterID,
			Op:         "checkpoint_barrier",
			ReasonCode: ReasonBarrierFailed,
			Diagnostic: fmt.Sprintf("begin core barrier failed: %v", err),
			Cause:      err,
		}
	}
	var releaseOnce sync.Once
	var releaseErr error
	releaseFn := func() error {
		releaseOnce.Do(func() {
			releaseErr = core.EndCheckpointBarrier(token)
		})
		return releaseErr
	}
	if flushHook != nil {
		if fErr := flushHook(ctx, viewID); fErr != nil {
			_ = releaseFn()
			return BarrierReceipt{}, nil, &AdapterError{
				AdapterID:  adapterID,
				Op:         "checkpoint_barrier",
				ReasonCode: ReasonBarrierFailed,
				Diagnostic: fmt.Sprintf("flush client dirty data failed: %v", fErr),
				Cause:      fErr,
			}
		}
	}
	return BarrierReceipt{
		ViewID:                  viewID,
		AdapterID:               adapterID,
		Token:                   token,
		WritersQuiesced:         true,
		OperationsDrained:       true,
		DirtyDataFlushed:        true,
		WritableMappingsFlushed: true,
	}, releaseFn, nil
}

func settleViewRelease(
	ctx context.Context,
	adapterID AdapterID,
	view *PreparedView,
	unmountFn func(ctx context.Context, viewID, runtimeDir string) error,
) (ReleaseReceipt, error) {
	receipt := ReleaseReceipt{ViewID: view.ViewID, AdapterID: adapterID}
	_ = view.Core.ReleaseView()
	if err := ctx.Err(); err != nil {
		receipt.Settled = false
		receipt.ReasonCode = ReasonCleanupFailed
		receipt.Diagnostic = fmt.Sprintf("deadline or cancellation is not proof of cleanup: %v", err)
		return receipt, &AdapterError{AdapterID: adapterID, Op: "release", ReasonCode: ReasonCleanupFailed, Diagnostic: receipt.Diagnostic, Cause: err}
	}
	if unmountFn != nil {
		if uErr := unmountFn(ctx, view.ViewID, view.RuntimeViewDir); uErr != nil {
			receipt.Settled = false
			receipt.ReasonCode = ReasonCleanupFailed
			receipt.Diagnostic = uErr.Error()
			return receipt, &AdapterError{AdapterID: adapterID, Op: "release", ReasonCode: ReasonCleanupFailed, Diagnostic: uErr.Error(), Cause: uErr}
		}
	}
	if err := os.RemoveAll(view.RuntimeViewDir); err != nil {
		receipt.Settled = false
		receipt.ReasonCode = ReasonCleanupFailed
		receipt.Diagnostic = err.Error()
		return receipt, &AdapterError{AdapterID: adapterID, Op: "release", ReasonCode: ReasonCleanupFailed, Diagnostic: err.Error(), Cause: err}
	}
	receipt.Settled = true
	receipt.CleanedPaths = []string{view.RuntimeViewDir}
	return receipt, nil
}
