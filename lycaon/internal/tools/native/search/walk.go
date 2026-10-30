package search

import (
	"github.com/lycaon/lycaon/internal/tools"
	"io/fs"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// entry is one visited path: its spelling as printed, its held-root binding
// and its own (or, when followed, its target's) metadata.
type entry struct {
	display string
	name    string
	bound   projectpaths.BoundPath
	stat    fseffect.EntryStat
	depth   int
	// start is the starting point the entry was reached from.
	start string
	// link reports that the entry itself is a symbolic link; followed that
	// it was one whose target now describes it.
	link, followed bool
	metadata       *entryMetadata
}

func (e entry) dir() bool  { return e.stat.Mode.IsDir() }
func (e entry) file() bool { return e.stat.Mode.IsRegular() }

// walker lists directories through the invocation's held roots. Every listed
// name counts against traversal_visits; going over refuses the operation.
// Entries the read boundary protects are not visited, as in the structured
// discovery tools.
type walker struct {
	inv     *invocation
	visited int64
	maximum int64
	filters map[string]sandbox.ReadFilter
	roots   map[string]*fseffect.ReadRoot
	lazy    bool
	work    discoveryWork
}

func newWalker(inv *invocation) *walker {
	return &walker{inv: inv, maximum: inv.owners.limit("traversal_visits"), filters: map[string]sandbox.ReadFilter{}, roots: map[string]*fseffect.ReadRoot{}}
}

func (w *walker) close() {
	for k, r := range w.roots {
		_ = r.Close()
		delete(w.roots, k)
	}
}

func (w *walker) readRoot(bound projectpaths.BoundPath) *fseffect.ReadRoot {
	path := bound.Resolved.Root.Path
	if path == "" || bound.Resolved.External {
		return nil
	}
	if err := bound.Validate(); err != nil {
		if old, ok := w.roots[path]; ok {
			_ = old.Close()
			delete(w.roots, path)
		}
		return nil
	}
	if r, ok := w.roots[path]; ok {
		return r
	}
	r, err := fseffect.OpenReadRoot(path)
	if err != nil {
		return nil
	}
	w.roots[path] = r
	return r
}

func (w *walker) visit() error {
	w.visited++
	if w.maximum > 0 && w.visited > w.maximum {
		return tools.CommandLimitExceeded("traversal_visits", w.maximum, w.visited)
	}
	return nil
}

// operand binds one command-line path. follow reports the target of a final
// contained link instead of the link.
func (w *walker) operand(spelling string, follow bool) (entry, error) {
	if err := w.visit(); err != nil {
		return entry{}, err
	}
	bound, err := w.inv.paths.Bind(w.inv.ctx, spelling, false)
	if err != nil {
		return entry{}, err
	}
	if bound.Null {
		return entry{}, w.inv.usage(spelling, "a directory entry, not the null device")
	}
	w.work.metadata++
	st, err := bound.Stat(false)
	if err != nil {
		return entry{}, err
	}
	e := entry{display: spelling, name: baseName(spelling), bound: bound, stat: st, start: spelling, link: st.Mode&fs.ModeSymlink != 0}
	if e.link && follow {
		w.work.metadata++
		if target, err := bound.Stat(true); err == nil {
			e.stat, e.link, e.followed = target, false, true
		}
	}
	return e, nil
}

// children lists a directory's admitted entries in unsigned byte order.
// follow reports the target metadata of contained links; a link whose
// target cannot be reached stays a link.
func (w *walker) children(dir entry, follow bool) ([]entry, error) {
	if entries, indexed, err := w.indexedChildren(dir, follow); indexed || err != nil {
		return entries, err
	}
	opened, err := dir.bound.OpenDirWithRoot(w.readRoot(dir.bound))
	if err != nil {
		return nil, err
	}
	defer opened.Close()
	names := opened.Names()
	w.work.listed++
	filter, err := w.filter(dir.bound)
	if err != nil {
		return nil, err
	}
	out := make([]entry, 0, len(names))
	for _, name := range names {
		if err := w.inv.ctx.Err(); err != nil {
			return nil, err
		}
		if err := w.visit(); err != nil {
			return nil, err
		}
		w.work.metadata++
		child, st, err := opened.StatChild(name)
		if err != nil {
			if _, ok := ordinary(err); ok {
				continue // removed between listing and inspection
			}
			return nil, err
		}
		display := child.Resolved.DisplayPath
		if dir.display == "." || dir.display != dir.bound.Resolved.DisplayPath {
			display = joinDisplay(dir.display, name)
		}
		e := entry{display: display, name: name, bound: child, stat: st, depth: dir.depth + 1, start: dir.start, link: st.Mode&fs.ModeSymlink != 0}
		if e.link && follow {
			w.work.metadata++
			if target, err := opened.StatChildFollowed(child); err == nil {
				e.stat, e.link, e.followed = target, false, true
			}
		}
		if filter != nil && !filter(child.Resolved.ScopeRel, e.dir()) {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// filter is the read boundary's protected-path predicate for a root.
func (w *walker) filter(bound projectpaths.BoundPath) (sandbox.ReadFilter, error) {
	root := bound.Resolved.Root.Path
	boundary := w.inv.owners.Revisions.Boundary
	if root == "" || bound.Resolved.External || boundary == nil {
		return nil, nil
	}
	if f, ok := w.filters[root]; ok {
		return f, nil
	}
	f, err := boundary.CompileReadFilter(w.inv.ctx, root, w.inv.tc.ProfileID())
	if err != nil {
		return nil, err
	}
	w.filters[root] = f
	return f, nil
}

// identity names a followed directory for cycle detection.
func identity(st fseffect.EntryStat) ([2]uint64, bool) {
	return [2]uint64{st.Device, st.Inode}, st.POSIX
}

func joinDisplay(dir, name string) string {
	switch {
	case dir == "":
		return name
	case strings.HasSuffix(dir, "/"):
		return dir + name
	}
	return dir + "/" + name
}

func baseName(spelling string) string {
	trimmed := strings.TrimRight(spelling, "/")
	if trimmed == "" {
		return spelling
	}
	if i := strings.LastIndexByte(trimmed, '/'); i >= 0 {
		return trimmed[i+1:]
	}
	return trimmed
}
