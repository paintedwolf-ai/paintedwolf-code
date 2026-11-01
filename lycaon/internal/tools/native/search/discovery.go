package search

import (
	"io/fs"
	"log/slog"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

type discoveryWork struct {
	metadata, listed, reused, prepared    int64
	roots, handles, relative, peak, opens int64
	identityPeak, identityHeld            int64
	reader                                *sourcecatalog.DirectoryReader
	readerRoot                            string
}

// Unknown kinds cannot authorize metadata, but policy can exclude both kinds.
func entryMayBeRead(filter sandbox.ReadFilter, rel string) bool {
	return filter == nil || filter(rel, false) || filter(rel, true)
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
	if w.work.reader != nil {
		w.closeDirectoryReader()
	}

	slog.InfoContext(w.inv.ctx, "command discovery completed",
		"execution_id", w.inv.request.Invocation.Identity.ExecutionID,
		"entries", w.visited, "entry_metadata_reads", w.work.metadata,
		"directory_live_lists", w.work.listed, "directory_reuses", w.work.reused,
		"directory_preparations", w.work.prepared, "shared_root_opens", w.work.roots,
		"directory_handles", w.work.handles, "held_directory_peak", w.work.peak, "held_directory_remaining", w.held, "relative_descriptor_opens", w.work.opens, "identity_descriptor_peak", w.work.identityPeak, "identity_descriptor_remaining", w.work.identityHeld, "relative_child_stats", w.work.relative)
}

func (w *walker) closeDirectoryReader() {
	if w.work.reader == nil {
		return
	}
	if err := w.work.reader.Close(w.inv.ctx); err != nil {
		slog.DebugContext(w.inv.ctx, "command catalog observation drain", "error", err)
	}
	w.work.reader = nil
}

type entryMetadata struct {
	known  bool
	stat   fseffect.EntryStat
	err    error
	parent *projectpaths.BoundDir
}

// Metadata is fetched only after a reached primary needs it.
func (w *walker) inspect(e entry) (entry, error) {
	if e.metadata == nil {
		return e, nil
	}
	m := e.metadata
	if !m.known {
		w.work.metadata++
		w.work.relative++
		_, m.stat, m.err = m.parent.StatChild(e.name)
		m.known = true
	}
	if m.err != nil {
		return e, m.err
	}
	e.stat = m.stat
	e.link = e.stat.Mode&fs.ModeSymlink != 0
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
	if w.work.reader == nil || w.work.readerRoot != root.Path {
		w.closeDirectoryReader()
		w.work.reader = catalog.Directories.NewDirectoryReader(w.inv.tc.Identity.ProjectID, sourcecatalog.Root{ID: root.ID, Path: root.Path})
		w.work.readerRoot = root.Path
	}
	reader := w.work.reader

	listing, err := reader.ReadDirectory(w.inv.ctx, dir.bound.Resolved.ScopeRel, maximum, nil)
	if listing.Prepared {
		w.work.prepared++
	}
	if err != nil {
		return nil, false, w.inv.ctx.Err()
	}
	if listing.Reused {
		w.work.reused++
	}
	filter, err := w.filter(dir.bound)
	if err != nil {
		return nil, true, err
	}
	out := make([]entry, 0, len(listing.Entries))
	for _, item := range listing.Entries {
		if err := w.inv.ctx.Err(); err != nil {
			return nil, true, err
		}
		if !opened.VisibleChild(item.Name) {
			continue
		}
		if err := w.visit(); err != nil {
			return nil, true, err
		}
		child := dir.bound.Child(item.Name)
		if !entryMayBeRead(filter, child.Resolved.ScopeRel) {
			continue
		}
		e := entry{display: joinDisplay(dir.display, item.Name), name: item.Name, bound: child,
			depth: dir.depth + 1, start: dir.start, link: item.IsSymlink}
		if !w.lazy {
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
		} else {
			if item.IsDir {
				e.stat.Mode = fs.ModeDir
			}
			if item.IsSymlink {
				e.stat.Mode = fs.ModeSymlink
			}
			e.metadata = &entryMetadata{parent: opened}
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
