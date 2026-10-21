package literal

import (
	"fmt"
	"io/fs"
	"os/user"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// lsOptions are ls's selections. Output is always one entry per line.
type lsOptions struct {
	long, all, almost, directory, recursive, human, classify, byTime, bySize, reverse bool
	slash, inode, blocks, deref, unsorted, changeTime, accessTime, groupDirs          bool
	// numeric is -n: owner and group stay decimal instead of account names.
	numeric bool
	// noGroup is GNU -G: long listings omit the group column.
	noGroup bool
	// noOwner is -g; commas is -m; followArgs is -H (command-line links);
	// physical is -P (never follow links, even command-line directory links).
	noOwner, commas, followArgs, physical bool
}

// time is the timestamp -c and -u select for display and sorting.
func (o lsOptions) time(st fseffect.EntryStat) time.Time {
	switch {
	case o.changeTime:
		return st.ChangeTime
	case o.accessTime:
		return st.AccessTime
	}
	return st.ModTime
}

// sortsByTime is -t, or -c/-u without -l, as GNU ls defines them.
func (o lsOptions) sortsByTime() bool {
	return !o.unsorted && (o.byTime || (o.changeTime || o.accessTime) && !o.long)
}

// lsColor accepts --color=never and auto: output is never a terminal, so
// both print plain names; always would need escape sequences ls never emits.
func lsColor(inv *invocation) error {
	option, ok := inv.argv.Last("--color")
	if !ok || len(option.Values) == 0 {
		return nil
	}
	switch option.Values[0].Value {
	case "never", "auto", "no", "none", "tty", "if-tty":
		return nil
	}
	return inv.usage(option.Values[0].Value, "--color=never or --color=auto")
}

// stat describes an entry, or with -L its link's target when reachable.
func (o lsOptions) stat(bound projectpaths.BoundPath) (fseffect.EntryStat, error) {
	st, err := bound.Stat(false)
	if err == nil && o.deref && typeLetter(st.Mode) == 'l' {
		if target, targetErr := bound.Stat(true); targetErr == nil {
			return target, nil
		}
	}
	return st, err
}

// lsEntry is one listed name with its own (not followed) metadata.
type lsEntry struct {
	name  string
	stat  fseffect.EntryStat
	bound projectpaths.BoundPath
}

func runLs(inv *invocation) error {
	opts := lsOptions{
		long: inv.flag("-l") || inv.flag("-n"), numeric: inv.flag("-n"), all: inv.flag("-a"), almost: inv.flag("-A"), directory: inv.flag("-d"), recursive: inv.flag("-R"),
		human: inv.flag("-h"), classify: inv.flag("-F"), byTime: inv.flag("-t"), bySize: inv.flag("-S"), reverse: inv.flag("-r"),
		slash: inv.flag("-p"), inode: inv.flag("-i"), blocks: inv.flag("-s"), unsorted: inv.flag("-U"),
		groupDirs: inv.flag("--group-directories-first"), noGroup: inv.flag("-G") || inv.flag("-o"),
		noOwner: inv.flag("-g"), commas: inv.flag("-m"),
	}
	opts.deref, opts.followArgs, opts.physical = lastLinkSelection(inv)
	// -o and -g are long listings without the group or the owner; -f is
	// -aU without long format, as GNU and BSD define them.
	opts.long = opts.long || inv.flag("-o") || inv.flag("-g")
	if inv.flag("-f") {
		opts.all, opts.unsorted, opts.long = true, true, false
	}
	opts.changeTime, opts.accessTime = lastTimeSelection(inv)
	if err := lsColor(inv); err != nil {
		return err
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
			st, err = opts.stat(bound)
			if err == nil && opts.followArgs && typeLetter(st.Mode) == 'l' {
				if target, targetErr := bound.Stat(true); targetErr == nil {
					st = target
				}
			}
		}
		if err == nil && !opts.directory && !opts.long && !opts.classify && !opts.physical && typeLetter(st.Mode) == 'l' {
			// A command-line link to a directory lists that directory unless
			// -l, -d, -F or -P describe the link itself.
			if followed, followErr := bound.Stat(true); followErr == nil && typeLetter(followed.Mode) == 'd' {
				st = followed
			}
		}
		if err != nil {
			err = projectpaths.PathOperationError(operand, err)
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
	if err := inv.listEntries(files, opts, false); err != nil {
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
	var walk func(label string, entry lsEntry, operand bool) error
	walk = func(label string, entry lsEntry, operand bool) error {
		children, readable, err := inv.children(entry.bound, opts, &visited, visits, operand)
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
		if !readable {
			return nil
		}
		sortEntries(children, opts)
		if err := inv.listEntries(children, opts, true); err != nil {
			return err
		}
		if !opts.recursive {
			return nil
		}
		for _, child := range children {
			if child.name == "." || child.name == ".." || typeLetter(child.stat.Mode) != 'd' {
				continue
			}
			if err := walk(path.Join(label, child.name), child, false); err != nil {
				return err
			}
		}
		return nil
	}
	for _, d := range dirEntries {
		if err := walk(d.name, d, true); err != nil {
			return err
		}
	}
	return nil
}

// children reads one directory's entries itself: no ignore rules or map.
// An unreadable directory is reported and yields no entries; GNU treats an
// unreadable command-line operand as serious (2) and a nested one as minor.
func (inv *invocation) children(dir projectpaths.BoundPath, opts lsOptions, visited *int64, maximum int64, operand bool) ([]lsEntry, bool, error) {
	names, err := dir.ReadDir()
	if err != nil {
		if reason, ok := ordinary(err); ok {
			_, _ = fmt.Fprintf(inv.stderr, "ls: cannot open directory '%s': %s\n", dir.Resolved.DisplayPath, reason)
			status := 1
			if operand {
				status = 2
			}
			inv.status = max(inv.status, status)
			return nil, false, nil
		}
		return nil, false, err
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
			return nil, false, limitExceeded("traversal_visits", maximum, *visited)
		}
		child := dir
		if name != "." {
			child = dir.Child(name)
		}
		st, err := opts.stat(child)
		if err != nil {
			if reason, ok := ordinary(err); ok {
				inv.fail(1, path.Join(dir.Resolved.DisplayPath, name), reason)
				continue
			}
			return nil, false, err
		}
		entry := lsEntry{name: name, stat: st, bound: child}
		out = append(out, entry)
	}
	if opts.all {
		if parent, err := inv.parentEntry(dir); err == nil {
			out = append(out, parent)
		}
	}
	return out, true, nil
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

// sortEntries uses unsigned byte order; -t (or -c/-u without -l) and -S
// sort descending with the name as a stable tie-break, -r reverses the
// final order and --group-directories-first then moves directories ahead.
// -U keeps byte order: directory order is not deterministic.
func sortEntries(entries []lsEntry, opts lsOptions) {
	byTime := opts.sortsByTime()
	slices.SortStableFunc(entries, func(a, b lsEntry) int {
		ta, tb := opts.time(a.stat), opts.time(b.stat)
		switch {
		case byTime && !ta.Equal(tb):
			return tb.Compare(ta)
		case opts.bySize && !byTime && !opts.unsorted && a.stat.Size != b.stat.Size:
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
	if opts.groupDirs {
		slices.SortStableFunc(entries, func(a, b lsEntry) int {
			da, db := typeLetter(a.stat.Mode) == 'd', typeLetter(b.stat.Mode) == 'd'
			switch {
			case da && !db:
				return -1
			case db && !da:
				return 1
			}
			return 0
		})
	}
}

// lastTimeSelection is the final of -c and -u, which override each other.
func lastTimeSelection(inv *invocation) (change, access bool) {
	for _, item := range inv.argv.Items {
		if item.Option == nil {
			continue
		}
		switch item.Option.Key {
		case "-c":
			change, access = true, false
		case "-u":
			change, access = false, true
		}
	}
	return change, access
}

// lastLinkSelection is the final of -L, -H and -P, which override each other.
func lastLinkSelection(inv *invocation) (deref, followArgs, physical bool) {
	for _, item := range inv.argv.Items {
		if item.Option == nil {
			continue
		}
		switch item.Option.Key {
		case "-L":
			deref, followArgs, physical = true, false, false
		case "-H":
			deref, followArgs, physical = false, true, false
		case "-P":
			deref, followArgs, physical = false, false, true
		}
	}
	return deref, followArgs, physical
}

// suffix is the -F or -p indicator for one entry.
func (o lsOptions) suffix(mode fs.FileMode) string {
	switch {
	case o.classify:
		return indicator(mode)
	case o.slash && typeLetter(mode) == 'd':
		return "/"
	}
	return ""
}

// allocated is -s's size column: 1 KiB units rounded up, or -h IEC size.
func (o lsOptions) allocated(st fseffect.EntryStat) string {
	if o.human {
		return humanSize(st.Blocks * 512)
	}
	return strconv.FormatInt((st.Blocks+1)/2, 10)
}

// prefixes are the -i and -s columns, right-aligned across one listing
// except in comma-separated (-m) listings, where GNU prints no padding.
func (o lsOptions) prefixes(entries []lsEntry) []string {
	if !o.inode && !o.blocks {
		return make([]string, len(entries))
	}
	inodes, sizes := make([]string, len(entries)), make([]string, len(entries))
	iw, sw := 0, 0
	for i, entry := range entries {
		inodes[i] = strconv.FormatUint(entry.stat.Inode, 10)
		sizes[i] = o.allocated(entry.stat)
		if !o.commas || o.long {
			iw, sw = max(iw, len(inodes[i])), max(sw, len(sizes[i]))
		}
	}
	out := make([]string, len(entries))
	for i := range entries {
		if o.inode {
			out[i] += fmt.Sprintf("%*s ", iw, inodes[i])
		}
		if o.blocks {
			out[i] += fmt.Sprintf("%*s ", sw, sizes[i])
		}
	}
	return out
}

func (o lsOptions) total(entries []lsEntry) string {
	blocks := int64(0)
	for _, entry := range entries {
		blocks += entry.stat.Blocks
	}
	if o.human {
		return humanSize(blocks * 512)
	}
	return strconv.FormatInt((blocks+1)/2, 10)
}

// listEntries prints one listing; a directory's long listing starts with
// GNU's total of allocated space (1 KiB units, or -h IEC size).
func (inv *invocation) listEntries(entries []lsEntry, opts lsOptions, total bool) error {
	if (opts.inode || opts.blocks) && slices.ContainsFunc(entries, func(e lsEntry) bool { return !e.stat.POSIX }) {
		return platformUnavailable("ls", "POSIX inode and allocation metadata")
	}
	prefixes := opts.prefixes(entries)
	if !opts.long {
		if total && opts.blocks {
			if err := inv.out.text("total " + opts.total(entries) + "\n"); err != nil {
				return err
			}
		}
		if opts.commas {
			return inv.commaList(entries, prefixes, opts)
		}
		for i, entry := range entries {
			if err := inv.out.text(prefixes[i] + entry.name + opts.suffix(entry.stat.Mode) + "\n"); err != nil {
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
		rows[i] = []string{modeString(entry.stat.Mode), strconv.FormatUint(entry.stat.Links, 10), inv.owners.accounts.owner(entry.stat.UID, opts.numeric), inv.owners.accounts.group(entry.stat.GID, opts.numeric), size}
		for column, value := range rows[i] {
			widths[column] = max(widths[column], len(value))
		}
	}
	if total {
		if err := inv.out.text("total " + opts.total(entries) + "\n"); err != nil {
			return err
		}
	}
	for i, entry := range entries {
		row := rows[i]
		name := entry.name
		if typeLetter(entry.stat.Mode) == 'l' {
			if target, err := entry.bound.Readlink(); err == nil {
				name += " -> " + target
			}
		} else {
			name += opts.suffix(entry.stat.Mode)
		}
		owner := fmt.Sprintf(" %-*s", widths[2], row[2])
		if opts.noOwner {
			owner = ""
		}
		group := fmt.Sprintf(" %-*s", widths[3], row[3])
		if opts.noGroup {
			group = ""
		}
		line := fmt.Sprintf("%s%s %*s%s%s %*s %s %s\n", prefixes[i], row[0], widths[1], row[1], owner, group, widths[4], row[4], opts.time(entry.stat).UTC().Format("2006-01-02 15:04"), name)
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

// accounts names owners and groups as GNU ls does, through the platform's
// account database, falling back to the decimal id when it has no name.
// Lookups are cached for the life of the owners.
type accounts struct {
	mu     sync.Mutex
	users  map[uint32]string
	groups map[uint32]string
}

func (a *accounts) owner(id uint32, numeric bool) string {
	return a.name(&a.users, id, numeric, func(text string) (string, error) {
		found, err := user.LookupId(text)
		if err != nil {
			return "", err
		}
		return found.Username, nil
	})
}

func (a *accounts) group(id uint32, numeric bool) string {
	return a.name(&a.groups, id, numeric, func(text string) (string, error) {
		found, err := user.LookupGroupId(text)
		if err != nil {
			return "", err
		}
		return found.Name, nil
	})
}

func (a *accounts) name(cache *map[uint32]string, id uint32, numeric bool, lookup func(string) (string, error)) string {
	decimal := strconv.FormatUint(uint64(id), 10)
	if numeric {
		return decimal
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if name, ok := (*cache)[id]; ok {
		return name
	}
	name, err := lookup(decimal)
	if err != nil || name == "" {
		name = decimal
	}
	if *cache == nil {
		*cache = map[uint32]string{}
	}
	(*cache)[id] = name
	return name
}

// commaList is -m: names separated by ", " and wrapped before column 80,
// GNU's width when output is not a terminal.
func (inv *invocation) commaList(entries []lsEntry, prefixes []string, opts lsOptions) error {
	const width = 80
	var b strings.Builder
	pos := 0
	for i, entry := range entries {
		name := prefixes[i] + entry.name + opts.suffix(entry.stat.Mode)
		if i > 0 {
			if pos+2+len(name) < width {
				b.WriteString(", ")
				pos += 2
			} else {
				b.WriteString(",\n")
				pos = 0
			}
		}
		b.WriteString(name)
		pos += len(name)
	}
	if len(entries) > 0 {
		b.WriteString("\n")
	}
	return inv.out.text(b.String())
}
