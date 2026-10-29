package literal

import (
	"strconv"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// duOptions are du's selections. unit is the last of -h, -k, -m and -b.
type duOptions struct {
	all, summarize, total, oneFS, apparent bool
	// depth is the deepest printed level below an operand; -1 prints all.
	depth int64
	unit  string
}

// duWalk is one du invocation's traversal state: the shared visit budget
// and the hard-linked files already counted.
type duWalk struct {
	inv     *invocation
	opts    duOptions
	visited int64
	maximum int64
	seen    map[[2]uint64]bool
	device  uint64
}

// runDu reports disk usage over each operand's tree, walked through the same
// bounded held-root directory reads as ls -R. A walk over traversal_visits
// refuses rather than report a short total.
func runDu(inv *invocation) error {
	opts, err := duSelections(inv)
	if err != nil {
		return err
	}
	operands := inv.operands()
	if len(operands) == 0 {
		operands = []string{"."}
	}
	walk := &duWalk{inv: inv, opts: opts, maximum: inv.owners.limit("traversal_visits"), seen: map[[2]uint64]bool{}}
	var grand int64
	for _, operand := range operands {
		bound, err := inv.paths.Bind(inv.ctx, operand, false)
		var st fseffect.EntryStat
		if err == nil {
			st, err = bound.Stat(false)
		}
		if err != nil {
			err = projectpaths.PathOperationError(operand, err)
			if reason, ok := ordinary(err); ok {
				inv.fail(1, operand, reason)
				continue
			}
			return err
		}
		walk.device = st.Device
		size, err := walk.entry(operand, bound, st, 0)
		if err != nil {
			return err
		}
		grand += size
	}
	if opts.total {
		return inv.out.text(walk.format(grand) + "\ttotal\n")
	}
	return nil
}

func duSelections(inv *invocation) (duOptions, error) {
	opts := duOptions{all: inv.flag("-a"), summarize: inv.flag("-s"), total: inv.flag("-c"), oneFS: inv.flag("-x"), depth: -1, unit: "k"}
	opts.apparent = inv.flag("--apparent-size") || inv.flag("-b")
	for _, item := range inv.argv.Items {
		if item.Option == nil {
			continue
		}
		switch key := item.Option.Key; key {
		case "-h", "-k", "-m", "-b":
			opts.unit = key[1:]
		}
	}
	if _, ok := inv.value("-d"); ok {
		depth, err := inv.integer("-d", 0, false)
		if err != nil {
			return opts, err
		}
		opts.depth = depth
	}
	switch {
	case opts.summarize && opts.all:
		return opts, inv.usage("-a", "either -s or -a, not both")
	case opts.summarize && opts.depth > 0:
		return opts, inv.usage("-s", "either -s or a nonzero -d depth, not both")
	case opts.summarize:
		opts.depth = 0
	}
	return opts, nil
}

// entry sizes one entry and, for a directory, everything below it, then
// prints it when its depth and kind are selected. Directories print after
// their contents.
func (w *duWalk) entry(display string, bound projectpaths.BoundPath, st fseffect.EntryStat, depth int64) (int64, error) {
	if !w.opts.apparent && !st.POSIX {
		return 0, platformUnavailable("du", "POSIX allocation metadata")
	}
	dir := typeLetter(st.Mode) == 'd'
	if !dir && st.Links > 1 && st.POSIX {
		key := [2]uint64{st.Device, st.Inode}
		if w.seen[key] {
			return 0, nil
		}
		w.seen[key] = true
	}
	size := st.Blocks * 512
	if w.opts.apparent {
		// A directory's own apparent size counts as zero.
		size = 0
		if !dir {
			size = st.Size
		}
	}
	if dir {
		children, _, err := w.inv.children(bound, lsOptions{almost: true}, &w.visited, w.maximum, depth == 0)
		if err != nil {
			return 0, err
		}
		for _, child := range children {
			if w.opts.oneFS && child.stat.Device != w.device {
				continue
			}
			childSize, err := w.entry(joinPath(display, child.name), child.bound, child.stat, depth+1)
			if err != nil {
				return 0, err
			}
			size += childSize
		}
	}
	shown := depth == 0 || (dir || w.opts.all) && (w.opts.depth < 0 || depth <= w.opts.depth)
	if shown {
		if err := w.inv.out.text(w.format(size) + "\t" + display + "\n"); err != nil {
			return 0, err
		}
	}
	return size, nil
}

// format renders a byte total in the selected unit, rounding up.
func (w *duWalk) format(size int64) string {
	switch w.opts.unit {
	case "h":
		return humanSize(size)
	case "b":
		return strconv.FormatInt(size, 10)
	case "m":
		return strconv.FormatInt((size+1<<20-1)>>20, 10)
	}
	return strconv.FormatInt((size+1023)>>10, 10)
}

// joinPath names a walked entry below its operand's spelling.
func joinPath(dir, name string) string {
	if dir != "" && dir[len(dir)-1] == '/' {
		return dir + name
	}
	return dir + "/" + name
}
