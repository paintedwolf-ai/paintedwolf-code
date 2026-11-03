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

// ActiveViewRecovery describes the preserved state of an active view after adapter failure.
type ActiveViewRecovery struct {
	ViewID                           string                    `json:"view_id"`
	OriginalAdapterID                AdapterID                 `json:"original_adapter_id"`
	Mutated                          bool                      `json:"mutated"`
	RequiresCheckpointReconstruction bool                      `json:"requires_checkpoint_reconstruction"`
	PreservedSnapshot                layeredfs.JournalSnapshot `json:"preserved_snapshot"`
	PreservedChanges                 []layeredfs.ChangeEntry   `json:"preserved_changes"`
	TransitionedView                 *PreparedView             `json:"transitioned_view,omitempty"`
}

type activeViewEntry struct {
	view    *PreparedView
	adapter Adapter
	failed  bool
	reason  ReasonCode
}

// ActiveViewRegistry enforces selection immutability for active views and preserves mutated state on failure.
type ActiveViewRegistry struct {
	mu    sync.RWMutex
	views map[string]*activeViewEntry
}

// NewActiveViewRegistry creates an ActiveViewRegistry.
func NewActiveViewRegistry() *ActiveViewRegistry {
	return &ActiveViewRegistry{views: make(map[string]*activeViewEntry)}
}

// Register binds a PreparedView to its selected Adapter.
func (r *ActiveViewRegistry) Register(view *PreparedView, adp Adapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.views[view.ViewID] = &activeViewEntry{view: view, adapter: adp}
}

// AdapterForView returns the fixed adapter and view for viewID if registered.
func (r *ActiveViewRegistry) AdapterForView(viewID string) (Adapter, *PreparedView, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.views[viewID]
	if !ok {
		return nil, nil, false
	}
	return e.adapter, e.view, true
}

// RecoverOrTransition handles adapter failure on an active view without erasing mutated worker data (#1658, #1666).
func (r *ActiveViewRegistry) RecoverOrTransition(
	ctx context.Context,
	viewID string,
	failureReason ReasonCode,
	replacement Adapter,
	allowCheckpointReconstruction bool,
) (*ActiveViewRecovery, error) {
	r.mu.Lock()
	entry, ok := r.views[viewID]
	if !ok {
		r.mu.Unlock()
		return nil, &AdapterError{Op: "recover_or_transition", ReasonCode: ReasonViewNotFound, Diagnostic: fmt.Sprintf("active view %q not found", viewID)}
	}
	entry.failed = true
	entry.reason = failureReason
	view, adp := entry.view, entry.adapter
	r.mu.Unlock()

	snap, err := view.Core.SnapshotJournal()
	if err != nil {
		return nil, err
	}
	changes, err := view.Core.EnumerateChanges(true)
	if err != nil {
		return nil, err
	}
	mutated := len(changes) > 0 || len(snap.Operations) > 0
	recovery := &ActiveViewRecovery{
		ViewID:                           viewID,
		OriginalAdapterID:                adp.ID(),
		Mutated:                          mutated,
		RequiresCheckpointReconstruction: mutated,
		PreservedSnapshot:                snap,
		PreservedChanges:                 changes,
	}
	if mutated && !allowCheckpointReconstruction {
		return recovery, &AdapterError{
			AdapterID:  adp.ID(),
			Op:         "recover_or_transition",
			ReasonCode: ReasonActiveViewMutated,
			Diagnostic: "refusing to switch mutated worker view to a fresh workspace without #1666 checkpoint reconstruction",
		}
	}
	if replacement != nil {
		probeRes, pErr := replacement.Probe(ctx, ProbeConfig{RuntimeDir: filepath.Dir(view.RuntimeViewDir)})
		if pErr != nil || !probeRes.Supported {
			return recovery, &AdapterError{
				AdapterID:  replacement.ID(),
				Op:         "recover_or_transition",
				ReasonCode: probeRes.ReasonCode,
				Diagnostic: "replacement adapter is unsupported; active worker data remains preserved in Core",
				Cause:      pErr,
			}
		}
	}
	return recovery, nil
}
