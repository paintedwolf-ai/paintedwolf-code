package search

import (
	"errors"
	"io/fs"
	"log/slog"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

type discoveryWork struct {
	metadata, listed, reused, prepared int64
}

func structuralPrimary(key string) bool {
	switch key {
	case "-name", "-iname", "-path", "-ipath", "-regex", "-iregex",
		"-maxdepth", "-mindepth", "-xdev", "-depth", "-true", "-false", "-prune":
		return true
	}
	return false
}

func (w *walker) report() {
	slog.InfoContext(w.inv.ctx, "command discovery completed",
		"execution_id", w.inv.request.Invocation.Identity.ExecutionID,
		"entries", w.visited, "entry_metadata_reads", w.work.metadata,
		"directory_live_lists", w.work.listed, "directory_reuses", w.work.reused,
		"directory_preparations", w.work.prepared)
}

type entryMetadata struct {
	known bool
	stat  fseffect.EntryStat
	err   error
}

// Metadata is fetched only after a reached primary needs it.
func (w *walker) inspect(e entry, follow bool) (entry, error) {
	if e.metadata == nil {
		return e, nil
	}
	m := e.metadata
	if !m.known {
		w.work.metadata++
		m.stat, m.err = e.bound.Stat(false)
		m.known = true
	}
	if m.err != nil {
		return e, m.err
	}
	e.stat = m.stat
	e.link = e.stat.Mode&fs.ModeSymlink != 0
	if e.link && follow {
		w.work.metadata++
		if target, err := e.bound.Stat(true); err == nil {
			e.stat, e.link, e.followed = target, false, true
		}
	}
	return e, nil
}

func (w *walker) indexedChildren(dir entry, follow bool) ([]entry, bool, error) {
	catalog := w.inv.owners.Catalog
	if follow || dir.followed || dir.bound.Resolved.External || catalog == nil || catalog.Directories == nil {
		return nil, false, nil
	}
	if err := dir.bound.Validate(); err != nil {
		return nil, true, err
	}
	file, err := dir.bound.Open()
	if err != nil {
		return nil, true, err
	}
	_ = file.Close()
	root := dir.bound.Resolved.Root
	maximum := 0
	if w.maximum > 0 {
		maximum = int(max(1, w.maximum-w.visited))
	}
	listing, err := catalog.Directories.ReadDirectory(w.inv.ctx, w.inv.tc.Identity.ProjectID,
		sourcecatalog.Root{ID: root.ID, Path: root.Path}, dir.bound.Resolved.ScopeRel, maximum)
	if listing.Reused {
		w.work.reused++
	} else if listing.Prepared {
		w.work.prepared++
	}
	if err != nil {
		var exceeded *sourcecatalog.DirectoryLimitError
		if errors.As(err, &exceeded) {
			w.visited = w.maximum
			return nil, true, w.visit()
		}
		return nil, false, w.inv.ctx.Err()
	}
	filter, err := w.filter(dir.bound)
	if err != nil {
		return nil, true, err
	}
	out := make([]entry, 0, len(listing.Entries))
	for _, item := range listing.Entries {
		if err := w.visit(); err != nil {
			return nil, true, err
		}
		child := dir.bound.Child(item.Name)
		mode := fs.FileMode(0)
		if item.IsDir {
			mode |= fs.ModeDir
		}
		if item.IsSymlink {
			mode = fs.ModeSymlink
		}
		e := entry{display: joinDisplay(dir.display, item.Name), name: item.Name, bound: child,
			stat: fseffect.EntryStat{Mode: mode}, depth: dir.depth + 1, start: dir.start,
			link: item.IsSymlink, metadata: &entryMetadata{}}
		if !w.lazy {
			e, err = w.inspect(e, false)
			if err != nil {
				if _, ok := ordinary(err); ok {
					continue
				}
				return nil, true, err
			}
		}
		if filter != nil && !filter(child.Resolved.ScopeRel, e.dir()) {
			continue
		}
		out = append(out, e)
	}
	return out, true, nil
}
