package scan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/project"
)

// ReconcileSpills removes spilled results and kept chunks whose scan no
// longer exists. Spills live under each project's host data tree, keyed by
// scan id; a scan retention pass releases the row, and this releases the
// bytes. It returns how many entries it removed.
func ReconcileSpills(ctx context.Context, dataDir string, store *SQLStore) (int, error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" || store == nil {
		return 0, nil
	}
	projects, err := os.ReadDir(filepath.Join(dataDir, project.HostProjectsDirName))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	removed := 0
	var errs []error
	for _, projectEntry := range projects {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		if !projectEntry.IsDir() {
			continue
		}
		spillDir := filepath.Join(dataDir, project.HostProjectsDirName, projectEntry.Name(), ScanResultSpillDir)
		entries, err := os.ReadDir(spillDir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return removed, err
			}
			scanID := strings.TrimSuffix(entry.Name(), ".json")
			if scanID == "" {
				continue
			}
			scan, err := store.Get(ctx, scanID)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if scan != nil {
				continue
			}
			if err := os.RemoveAll(filepath.Join(spillDir, entry.Name())); err != nil {
				errs = append(errs, err)
				continue
			}
			removed++
		}
	}
	return removed, errors.Join(errs...)
}
