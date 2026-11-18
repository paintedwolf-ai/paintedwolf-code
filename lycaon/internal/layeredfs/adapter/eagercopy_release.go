package adapter

import "context"

func (st *eagerViewState) checkAdmission(op string) error {
	if !st.releasing.Load() {
		return nil
	}
	return &AdapterError{AdapterID: AdapterEagerCopy, Op: op, ReasonCode: ReasonCleanupFailed, Diagnostic: "materialized view release is incomplete"}
}

// Release retains retry state until durability and resource cleanup settle.
func (a *EagerCopyAdapter) Release(ctx context.Context, viewID string) (ReleaseReceipt, error) {
	receipt := ReleaseReceipt{ViewID: viewID, AdapterID: AdapterEagerCopy}
	st, ok := a.lookupViewState(viewID)
	if !ok {
		receipt.ReasonCode = ReasonViewNotFound
		return receipt, &AdapterError{AdapterID: AdapterEagerCopy, Op: "release", ReasonCode: ReasonViewNotFound, Diagnostic: viewID}
	}
	st.releaseMu.Lock()
	defer st.releaseMu.Unlock()
	st.releasing.Store(true)
	st.syncMu.Lock()
	defer st.syncMu.Unlock()
	if !st.settled {
		if err := st.settleRelease(ctx); err != nil {
			receipt.ReasonCode = ReasonCleanupFailed
			receipt.Diagnostic = err.Error()
			return receipt, &AdapterError{AdapterID: AdapterEagerCopy, Op: "release", ReasonCode: ReasonCleanupFailed, Diagnostic: err.Error(), Cause: err}
		}
		st.settled = true
		sharedEagerViews.CompareAndDelete(viewID, st)
	}
	receipt.Settled = true
	receipt.CleanedPaths = []string{st.view.RuntimeViewDir}
	return receipt, nil
}

func (st *eagerViewState) settleRelease(ctx context.Context) error {
	if err := st.view.Core.ReleaseView(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, root := range st.view.Roots {
		if root.WorkspacePath != "" {
			makeDirsWritable(root.WorkspacePath)
		}
	}
	if err := removeAllForceWritable(st.view.RuntimeViewDir); err != nil {
		return err
	}
	for len(st.gitLeases) > 0 {
		if err := st.gitStore.ReleaseLease(st.gitLeases[0]); err != nil {
			return err
		}
		st.gitLeases = st.gitLeases[1:]
	}
	return nil
}
