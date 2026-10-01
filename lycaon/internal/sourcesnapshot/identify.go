package sourcesnapshot

import (
	"context"
	"errors"
)

// Identify reads one named live file after a reported change. Matching stat
// metadata cannot validate bytes a watcher has already invalidated. It
// reports false for a path that is absent or not a regular file. A caller
// observing a few named paths uses it instead of publishing a generation.
func (s *Store) Identify(ctx context.Context, rootPath, rel string) (Entry, bool, error) {
	if s == nil {
		return Entry{}, false, errors.New("source snapshot store is not configured")
	}
	rel = cleanRelDir(rel)
	baseline, err := s.newObserved(ctx, rootPath)
	if err != nil {
		return Entry{}, false, err
	}
	defer baseline.close()
	entry, err := s.identify(ctx, baseline, &identifier{root: rootPath}, fileRef{Path: rel, Abs: absPath(rootPath, rel)}, VerifyContent)
	switch {
	case errors.Is(err, errVanished), errors.Is(err, errSkipped):
		return Entry{}, false, nil
	case err != nil:
		return Entry{}, false, err
	}
	return entry, true, baseline.flush(ctx, s)
}
