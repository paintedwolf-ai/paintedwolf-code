package db

import (
	"context"
	"fmt"
	"path/filepath"
)

// Directory resolves installation-owned runtime paths from the active main database.
func Directory(ctx context.Context, database DBTX) (string, error) {
	if store, ok := database.(*Store); ok && store.dataDir != "" {
		return store.dataDir, nil
	}
	var path string
	if err := database.QueryRowContext(ctx, `SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("durable storage requires an on-disk main database")
	}
	return filepath.Dir(path), nil
}
