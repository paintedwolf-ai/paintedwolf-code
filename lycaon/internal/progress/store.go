package progress

import (
	"context"
	"database/sql"
	"strings"
)

// AckOutcome reports transaction-scoped worker handle resolution and accounting.
type AckOutcome struct {
	AccountedJobIDs []string
	UnknownHandles  []string
}

// TxReconciler coordinates durable result accounting inside the progress transaction
// and releases in-memory latches after commit.
type TxReconciler interface {
	Reconciliation(root string) *Reconciliation
	UnaccountedResults(root string) []UnaccountedResult
	AcknowledgeTx(ctx context.Context, tx *sql.Tx, root, principalID string, handles []string) (AckOutcome, error)
	Release(root string, jobIDs []string)
}

// ApplyOptions carries optional host context for Store.Apply.
type ApplyOptions struct {
	PrincipalID string
	RunID       string
	Basis       PreparedProgressBasis
	Reconciler  TxReconciler
}

// Store persists root-session progress rows and workflow run bindings.
type Store interface {
	Snapshot(ctx context.Context, sessionID string) (Snapshot, error)
	Apply(ctx context.Context, sessionID string, mutation Mutation, opts ApplyOptions) (MutationResult, error)
	BindRun(ctx context.Context, sessionID, runID string, resetOnTerminal bool) (bool, error)
	SeedIfMissing(ctx context.Context, sessionID, runID string, rows []RowSpec) (Snapshot, error)
	Clear(ctx context.Context, sessionID string) error
}

func normalizeKey(sessionID string) string {
	return strings.TrimSpace(sessionID)
}
