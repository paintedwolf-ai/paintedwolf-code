package literal

import (
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// lsOptions are ls's selections. Output is always one entry per line.
type lsOptions struct {
	long, all, almost, directory, recursive, human, classify, byTime, bySize, reverse bool
}

// lsEntry is one listed name with its own (not followed) metadata.
type lsEntry struct {
	name  string
	stat  fseffect.EntryStat
	bound projectpaths.BoundPath
}

func runLs(inv *invocation) error {
	opts := lsOptions{
		long: inv.flag("-l"), all: inv.flag("-a"), almost: inv.flag("-A"), directory: inv.flag("-d"), recursive: inv.flag("-R"),
		human: inv.flag("-h"), classify: inv.flag("-F"), byTime: inv.flag("-t"), bySize: inv.flag("-S"), reverse: inv.flag("-r"),
	}
	operands := inv.operands()
	if len(operands) == 0 {
		operands = []string{"."}
	}
	var files []lsEntry
	type dir struct {
		label string
		entry lsEntry
	}
	var dirs []dir
	for _, operand := range operands {
		bound, err := inv.paths.Bind(inv.ctx, operand, false)
		var st fseffect.EntryStat
		if err == nil {
			st, err = bound.Stat(false)
		}
		if err == nil && !opts.directory && !opts.long && !opts.classify && typeLetter(st.Mode) == 'l' {
			// A command-line link to a directory lists that directory unless
			// -l, -d or -F describe the link itself.
			if followed, followErr := bound.Stat(true); followErr == nil && typeLetter(followed.Mode) == 'd' {
				st = followed
			}
		}
		if err != nil {
			if reason, ok := ordinary(err); ok {
				inv.fail(2, operand, reason)
				continue
			}
			return err
		}
		entry := lsEntry{name: operand, stat: st, bound: bound}
		if typeLetter(st.Mode) == 'd' && !opts.directory {
			dirs = append(dirs, dir{label: operand, entry: entry})
		} else {
			files = append(files, entry)
		}
	}
	sortEntries(files, opts)
	if err := inv.listEntries(files, opts); err != nil {
		return err
	}
	dirEntries := make([]lsEntry, len(dirs))
	for i, d := range dirs {
		dirEntries[i] = d.entry
	}
	sortEntries(dirEntries, opts)
	visits := inv.owners.limit("traversal_visits")
	visited := int64(0)
	labelled := len(files) > 0 || len(dirEntries) > 1 || opts.recursive
	first := len(files) == 0
	var walk func(label string, entry lsEntry) error
	walk = func(label string, entry lsEntry) error {
		children, err := inv.children(entry.bound, opts, &visited, visits)
		if err != nil {
			return err
		}
		if !first {
			if err := inv.out.text("\n"); err != nil {
				return err
			}
		}
		first = false
		if labelled {
			if err := inv.out.text(label + ":\n"); err != nil {
				return err
			}
		}
		if children == nil {
			return nil
		}
		sortEntries(children, opts)
		if err := inv.listEntries(children, opts); err != nil {
			return err
		}
		if !opts.recursive {
			return nil
		}
		for _, child := range children {
			if child.name == "." || child.name == ".." || typeLetter(child.stat.Mode) != 'd' {
				continue
			}
			if err := walk(path.Join(label, child.name), child); err != nil {
				return err
			}
		}
		return nil
	}
	for _, d := range dirEntries {
		if err := walk(d.name, d); err != nil {
			return err
		}
	}
	return nil
}

// children reads one directory's entries itself: no ignore rules or map.
// An unreadable directory is reported and yields no entries.
func (inv *invocation) children(dir projectpaths.BoundPath, opts lsOptions, visited *int64, maximum int64) ([]lsEntry, error) {
	names, err := dir.ReadDir()
	if err != nil {
		if reason, ok := ordinary(err); ok {
			_, _ = fmt.Fprintf(inv.stderr, "ls: cannot open directory '%s': %s\n", dir.Resolved.DisplayPath, reason)
			inv.status = max(inv.status, 1)
			return nil, nil
		}
		return nil, err
	}
	if opts.all {
		names = append([]string{"."}, names...)
	}
	var out []lsEntry
	for _, name := range names {
		if !opts.all && !opts.almost && strings.HasPrefix(name, ".") {
			continue
		}
		*visited++
		if maximum > 0 && *visited > maximum {
			return nil, limitExceeded("traversal_visits", maximum, *visited)
		}
		child := dir
		if name != "." {
			child = dir.Child(name)
		}
		st, err := child.Stat(false)
		if err != nil {
			if reason, ok := ordinary(err); ok {
				inv.fail(1, path.Join(dir.Resolved.DisplayPath, name), reason)
				continue
			}
			return nil, err
		}
		entry := lsEntry{name: name, stat: st, bound: child}
		out = append(out, entry)
	}
	if opts.all {
		if parent, err := inv.parentEntry(dir); err == nil {
			out = append(out, parent)
		}
	}
	return out, nil
}

// parentEntry describes ".." through the parent's own binding when it is
// inside the same admitted root; it is never followed outward.
func (inv *invocation) parentEntry(dir projectpaths.BoundPath) (lsEntry, error) {
	parent, err := inv.paths.Bind(inv.ctx, path.Join(dir.Resolved.Abs, ".."), false)
	if err != nil {
		return lsEntry{}, err
	}
	st, err := parent.Stat(false)
	if err != nil {
		return lsEntry{}, err
	}
	return lsEntry{name: "..", stat: st, bound: parent}, nil
}

// sortEntries uses unsigned byte order; -t and -S sort descending with the
// name as a stable tie-break, and -r reverses the final order.
func sortEntries(entries []lsEntry, opts lsOptions) {
	slices.SortStableFunc(entries, func(a, b lsEntry) int {
		switch {
		case opts.byTime && !a.stat.ModTime.Equal(b.stat.ModTime):
			return b.stat.ModTime.Compare(a.stat.ModTime)
		case opts.bySize && !opts.byTime && a.stat.Size != b.stat.Size:
			if a.stat.Size > b.stat.Size {
				return -1
			}
			return 1
		}
		return strings.Compare(a.name, b.name)
	})
	if opts.reverse {
		slices.Reverse(entries)
	}
}

func (inv *invocation) listEntries(entries []lsEntry, opts lsOptions) error {
	if !opts.long {
		for _, entry := range entries {
			name := entry.name
			if opts.classify {
				name += indicator(entry.stat.Mode)
			}
			if err := inv.out.text(name + "\n"); err != nil {
				return err
			}
		}
		return nil
	}
	rows := make([][]string, len(entries))
	widths := make([]int, 5)
	for i, entry := range entries {
		if !entry.stat.POSIX {
			return platformUnavailable("ls", "POSIX owner and link metadata")
		}
		size := strconv.FormatInt(entry.stat.Size, 10)
		if opts.human {
			size = humanSize(entry.stat.Size)
		}
		rows[i] = []string{modeString(entry.stat.Mode), strconv.FormatUint(entry.stat.Links, 10), strconv.FormatUint(uint64(entry.stat.UID), 10), strconv.FormatUint(uint64(entry.stat.GID), 10), size}
		for column, value := range rows[i] {
			widths[column] = max(widths[column], len(value))
		}
	}
	for i, entry := range entries {
		row := rows[i]
		name := entry.name
		if typeLetter(entry.stat.Mode) == 'l' {
			if target, err := entry.bound.Readlink(); err == nil {
				name += " -> " + target
			}
		} else if opts.classify {
			name += indicator(entry.stat.Mode)
		}
		line := fmt.Sprintf("%s %*s %-*s %-*s %*s %s %s\n", row[0], widths[1], row[1], widths[2], row[2], widths[3], row[3], widths[4], row[4], entry.stat.ModTime.UTC().Format("2006-01-02 15:04"), name)
		if err := inv.out.text(line); err != nil {
			return err
		}
	}
	return nil
}

// humanSize is GNU ls -h: powers of 1024, one decimal below ten, rounded up.
func humanSize(size int64) string {
	if size < 1024 {
		return strconv.FormatInt(size, 10)
	}
	value := float64(size)
	units := "KMGTPE"
	unit := -1
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if value < 10 {
		tenths := int64(value * 10)
		if float64(tenths) < value*10 {
			tenths++
		}
		if tenths < 100 {
			return fmt.Sprintf("%d.%d%c", tenths/10, tenths%10, units[unit])
		}
		value = float64(tenths) / 10
	}
	whole := int64(value)
	if float64(whole) < value {
		whole++
	}
	if whole >= 1024 && unit < len(units)-1 {
		return fmt.Sprintf("1.0%c", units[unit+1])
	}
	return fmt.Sprintf("%d%c", whole, units[unit])
}
