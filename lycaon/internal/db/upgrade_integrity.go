package db

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lycaon/lycaon/internal/db/migrations"
)

func validateUpgradeStructure(ctx context.Context, database *sql.DB) (err error) {
	connection, err := database.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, connection.Close()) }()
	return migrations.WithIntegrityCache(ctx, connection, func() error {
		return validateStoreStructure(ctx, connection)
	})
}
