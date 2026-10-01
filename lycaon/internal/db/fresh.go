package db

import (
	"errors"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
)

// FreshRequested reports whether an empty local database was requested.
func FreshRequested() bool {
	return configdir.EnvTruthy(os.Getenv("LYCAON_DB_FRESH"))
}

// FreshEnabled limits database deletion to development storage.
func FreshEnabled() bool {
	if !FreshRequested() || !configdir.IsDevelopmentBuild() {
		return false
	}
	if strings.TrimSpace(os.Getenv(configdir.EnvConfigDir)) != "" {
		return true
	}
	return configdir.IsDevelopmentChannel()
}

// RemoveStore deletes the database and its SQLite journals.
func RemoveStore(dbPath string) error {
	if strings.TrimSpace(dbPath) == "" {
		return nil
	}
	var errs []error
	if err := os.Remove(dbPath); err != nil && !os.IsNotExist(err) {
		errs = append(errs, err)
	}
	errs = append(errs, RemoveStoreSidecars(dbPath))
	return errors.Join(errs...)
}
