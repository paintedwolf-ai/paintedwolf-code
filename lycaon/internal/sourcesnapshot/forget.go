package sourcesnapshot

import "context"

// forgetBatch bounds one delete of vanished cache rows.
const forgetBatch = 1024

// forgetVanished removes cached paths absent from a complete survey.
func (s *Store) forgetVanished(ctx context.Context, c *rootCandidate) error {
	for _, b := range c.boundaries {
		if b.Budgeted() {
			return nil
		}
	}
	admitted, err := s.db.QueryContext(ctx, `
SELECT path FROM source_manifest_staging WHERE build_id = ? AND root_path = ? ORDER BY path`, c.build, c.root.Path)
	if err != nil {
		return err
	}
	defer func() { _ = admitted.Close() }()
	next := func() (string, bool, error) {
		if !admitted.Next() {
			return "", false, admitted.Err()
		}
		var path string
		err := admitted.Scan(&path)
		return path, err == nil, err
	}
	current, more, err := next()
	if err != nil {
		return err
	}
	var stale []string
	err = s.observations.ForEachPath(ctx, c.root.Path, func(observed string) error {
		for more && current < observed {
			var err error
			if current, more, err = next(); err != nil {
				return err
			}
		}
		if !more || current != observed {
			stale = append(stale, observed)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := admitted.Err(); err != nil {
		return err
	}
	return s.forgetPaths(ctx, c.root.Path, stale)
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
