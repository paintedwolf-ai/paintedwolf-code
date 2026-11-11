package hitl

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/db"
)

// Authority replay shares one read snapshot while compensation uses the writer.
func (s *SQLStore) withAuthoritySnapshot(ctx context.Context, visit func(authoritySnapshot) error) error {
	reader, ok := s.db.(db.ReadHandle)
	if !ok {
		return fmt.Errorf("checkpoint replay requires read snapshots")
	}
	tx, err := reader.BeginReadTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := visit(authoritySnapshot{queries: s.queries.WithTx(tx)}); err != nil {
		return err
	}
	return tx.Commit()
}
