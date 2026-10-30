package search

import (
	"errors"
	"io/fs"
	"log/slog"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

type discoveryWork struct {
	metadata, listed, reused, prepared int64
	roots, handles, relative           int64
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
		"directory_preparations", w.work.prepared, "shared_root_opens", w.work.roots,
		"directory_handles", w.work.handles, "relative_child_stats", w.work.relative)
}

type entryMetadata struct {
	known  bool
	stat   fseffect.EntryStat
	err    error
	parent *projectpaths.BoundPath
}

// Metadata is fetched only after a reached primary needs it.
func (w *walker) inspect(e entry, follow bool) (entry, error) {
	if e.metadata == nil {
		return e, nil
	}
	m := e.metadata
	if !m.known {
		w.work.metadata++
		if m.parent == nil {
			m.stat, m.err = e.bound.Stat(false)
		} else {
			opened, err := m.parent.OpenDirWithRoot(w.readRoot(*m.parent))
			m.err = err
			if err == nil {
				w.work.handles++
				w.work.relative++
				_, m.stat, m.err = opened.StatChild(e.name)
				_ = opened.Close()
			}
		}
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

func (w *walker) indexedChildren(dir entry, follow bool, opened *projectpaths.BoundDir) ([]entry, bool, error) {
	catalog := w.inv.owners.Catalog
	if follow || dir.followed || dir.bound.Resolved.External || catalog == nil || catalog.Directories == nil {
		return nil, false, nil
	}
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
			link: item.IsSymlink, metadata: &entryMetadata{parent: &dir.bound}}
		if !w.lazy || item.IsDir || item.IsSymlink {
			w.work.metadata++
			w.work.relative++
			var stat fseffect.EntryStat
			child, stat, err = opened.StatChild(item.Name)
			if err != nil {
				if _, ok := ordinary(err); ok {
					continue
				}
				return nil, true, err
			}
			e.bound, e.stat, e.metadata = child, stat, nil
			e.link = stat.Mode&fs.ModeSymlink != 0
		}
		e.display = child.Resolved.DisplayPath
		if dir.display == "." || dir.display != dir.bound.Resolved.DisplayPath {
			e.display = joinDisplay(dir.display, item.Name)
		}
		if filter != nil && !filter(child.Resolved.ScopeRel, e.dir()) {
			continue
		}
		out = append(out, e)
	}
	return out, true, nil
}
