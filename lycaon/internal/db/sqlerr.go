package db

import (
	"database/sql"
	"errors"
)

// IsNoRows recognizes direct and wrapped sql.ErrNoRows.
func IsNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
