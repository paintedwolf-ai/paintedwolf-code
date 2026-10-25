package sourcesnapshot

import (
	"context"

	"github.com/lycaon/lycaon/internal/db"
)

// forgetBatch bounds one delete of vanished cache rows.
const forgetBatch = 1024

// forgetVanished removes cached paths absent from a complete survey. It
// merges the observation index with the staged survey a page at a time.
func (s *Store) forgetVanished(ctx context.Context, c *rootCandidate) error {
	for _, b := range c.boundaries {
		if b.Budgeted() {
			return nil
		}
	}
	admitted := stagedPaths{store: s, build: c.build}
	var stale []string
	err := s.observations.ForEachPath(ctx, c.root.Path, func(observed string) error {
		held, err := admitted.holds(ctx, observed)
		if err != nil {
			return err
		}
		if !held {
			stale = append(stale, observed)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return s.forgetPaths(ctx, c.root.Path, stale)
}

// stagedPaths answers membership for paths asked in ascending order.
type stagedPaths struct {
	store *Store
	build string
	page  []string
	after string
	done  bool
}

func (p *stagedPaths) holds(ctx context.Context, path string) (bool, error) {
	for {
		for len(p.page) > 0 && p.page[0] < path {
			p.page = p.page[1:]
		}
		if len(p.page) > 0 {
			return p.page[0] == path, nil
		}
		if p.done {
			return false, nil
		}
		rows, err := p.store.queries.ListSourceManifestStagingPage(ctx, db.ListSourceManifestStagingPageParams{
			BuildID: p.build, After: p.after, PageSize: stagingPage,
		})
		if err != nil {
			return false, err
		}
		for _, row := range rows {
			p.page = append(p.page, row.Path)
		}
		p.done = len(rows) < stagingPage
		if len(rows) > 0 {
			p.after = rows[len(rows)-1].Path
		}
	}
}

// forgetPaths drops cache rows in bounded batches.
func (s *Store) forgetPaths(ctx context.Context, rootPath string, paths []string) error {
	for len(paths) > 0 {
		batch := paths[:min(forgetBatch, len(paths))]
		paths = paths[len(batch):]
		if err := s.observations.DeletePaths(ctx, rootPath, batch); err != nil {
			return err
		}
	}
	return nil
}
