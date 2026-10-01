package contentblob

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/lycaon/lycaon/internal/bloblifecycle"
	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/db"
)

// DensityDeps names the live resources one density pass reads.
type DensityDeps struct {
	Queries *db.Queries
	DataDir string
}

// RunDensity recompresses idle projects' hot content blobs immediately and on
// cfg's ticker until ctx is done.
func RunDensity(ctx context.Context, deps DensityDeps, cfg DensityConfig) error {
	if err := RunDensityPass(ctx, deps, cfg); err != nil {
		return err
	}
	ticker := time.NewTicker(cfg.PollInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := RunDensityPass(ctx, deps, cfg); err != nil {
				return err
			}
		}
	}
}

// RunDensityPass recompresses bounded batches for idle hot projects.
func RunDensityPass(ctx context.Context, deps DensityDeps, cfg DensityConfig) error {
	lifecycle := bloblifecycle.ForDevice(deps.DataDir)
	if !lifecycle.TryLock() {
		return nil
	}
	defer lifecycle.Unlock()
	cutoff := db.FormatTime(time.Now().UTC().Add(-cfg.IdleThreshold()))
	projectIDs, err := deps.Queries.ListIdleHotProjects(ctx, db.ListIdleHotProjectsParams{
		LastOpenedAt: cutoff, Limit: int64(cfg.ProjectsPerTick()),
	})
	if err != nil {
		return fmt.Errorf("list idle hot projects: %w", err)
	}
	for _, projectID := range projectIDs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := densifyProject(ctx, deps, cfg, projectID); err != nil {
			return fmt.Errorf("densify project %s: %w", projectID, err)
		}
	}
	return nil
}

func densifyProject(ctx context.Context, deps DensityDeps, cfg DensityConfig, projectID string) error {
	rows, err := deps.Queries.ListHotContentBlobObjectsForProject(ctx, db.ListHotContentBlobObjectsForProjectParams{
		ProjectID: projectID, Limit: int64(cfg.BlobsPerProjectTick()),
	})
	if err != nil {
		return fmt.Errorf("list hot content blob objects: %w", err)
	}
	store := StoreFor(deps.DataDir, projectID)
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := densifyOne(ctx, deps, store, projectID, row); err != nil {
			return err
		}
	}
	remaining, err := deps.Queries.CountHotContentBlobObjectsForProject(ctx, projectID)
	if err != nil {
		return fmt.Errorf("count hot content blob objects: %w", err)
	}
	if remaining == 0 {
		if err := deps.Queries.MarkProjectStorageTierCold(ctx, projectID); err != nil {
			return fmt.Errorf("mark project storage tier cold: %w", err)
		}
	}
	return nil
}

func densifyOne(ctx context.Context, deps DensityDeps, store blobstore.Store, projectID string, row db.ListHotContentBlobObjectsForProjectRow) error {
	rel, err := RelPath(row.Sha256)
	if err != nil {
		return err
	}
	changed, err := store.Recompress(blobstore.Blob{Rel: rel})
	if err != nil {
		return fmt.Errorf("recompress %s: %w", row.Sha256, err)
	}
	storedSize := row.StoredSize
	if changed {
		info, statErr := os.Stat(filepath.Join(store.Root, filepath.FromSlash(rel)))
		if statErr != nil {
			return fmt.Errorf("stat recompressed blob: %w", statErr)
		}
		storedSize = info.Size()
	}
	if err := deps.Queries.MarkContentBlobObjectCold(ctx, db.MarkContentBlobObjectColdParams{
		StoredSize: storedSize, ProjectID: projectID, Sha256: row.Sha256,
	}); err != nil {
		return fmt.Errorf("mark content blob object cold: %w", err)
	}
	return nil
}
