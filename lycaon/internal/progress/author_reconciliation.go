package progress

import "context"

func (u AuthorUpdate) ExpectReconciliation(token string) AuthorUpdate {
	u.reconciliationToken = token
	return u
}

func (u AuthorUpdate) validateReconciliation(ctx context.Context, store Store, root string, reconciler Reconciler) error {
	if u.reconciliationToken == "" {
		return nil
	}
	var pending *Reconciliation
	if reconciler != nil {
		pending = reconciler.Reconciliation(root)
	}
	if pending != nil && pending.Token == u.reconciliationToken {
		return nil
	}
	current, err := store.Snapshot(ctx, root)
	if err != nil {
		return err
	}
	current.Reconciliation = pending
	return &AuthorUpdateRejection{Code: "PROGRESS_RECONCILIATION_CHANGED", Data: map[string]any{"snapshot": current}}
}
