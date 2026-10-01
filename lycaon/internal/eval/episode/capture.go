package episode

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/db"
)

func openCapture(ctx context.Context, capture string) (*sql.DB, error) {
	path := filepath.Join(capture, "store.db")
	if info, err := os.Stat(path + "-wal"); err == nil && info.Size() != 0 {
		return nil, fmt.Errorf("application evidence has uncheckpointed writes")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return db.OpenReadOnly(ctx, path)
}
