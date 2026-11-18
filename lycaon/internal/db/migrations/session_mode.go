package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

const (
	revision6ID       = "session-mode-authority"
	revision6Checksum = "77168ac8ec2e74b3a9408abffc7b7bed8bbd39e918a51f47c3f938c100b693b2"
)

//go:embed session_mode.sql
var sessionModeSQL []byte

// Revision6 assigns durable root authority to each chat tree.
func Revision6(target Baseline) Step {
	return Step{
		ID: revision6ID, Checksum: revision6Checksum,
		From: releasedRevision5, To: target,
		Source: sessionModeSQL, RebuildTables: true, Writes: sessionModeWrites,
		Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, string(sessionModeSQL))
			return err
		},
	}
}
