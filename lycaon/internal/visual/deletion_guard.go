package visual

import (
	"context"
	"database/sql"
)

type deletionGuardKey struct{}

// WithDeletionGuard rechecks host-owned eligibility in the deletion transaction.
func WithDeletionGuard(ctx context.Context, guard func(context.Context, *sql.Tx) error) context.Context {
	return context.WithValue(ctx, deletionGuardKey{}, guard)
}

func guardDeletion(ctx context.Context, tx *sql.Tx) error {
	if guard, ok := ctx.Value(deletionGuardKey{}).(func(context.Context, *sql.Tx) error); ok {
		return guard(ctx, tx)
	}
	return nil
}
