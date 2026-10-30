package literal

import (
	"errors"
	"fmt"
	"log/slog"
	"path"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// directoryListingWork records membership and metadata work for one command.
type directoryListingWork struct {
	entries, directories, metadata int64
}

func (work *directoryListingWork) report(inv *invocation) {
	slog.InfoContext(inv.ctx, "command directory listing completed",
		"execution_id", inv.request.Invocation.Identity.ExecutionID,
		"command", inv.name(), "entries", work.entries,
		"directories", work.directories, "entry_metadata_reads", work.metadata)
}

func (o lsOptions) needsMetadata() bool {
	return o.long || o.recursive || o.classify || o.slash || o.inode || o.blocks || o.sortsByTime() || o.bySize && !o.unsorted || o.groupDirs
}

func (inv *invocation) children(dir projectpaths.BoundPath, opts lsOptions, visited *int64, maximum int64, operand bool) ([]lsEntry, bool, error) {
	metadata := inv.name() != "ls" || opts.needsMetadata()
	inv.directoryWork.directories++
	opened, err := dir.OpenDir()
	var names []string
	if err == nil {
		nameLimit := 0
		if maximum > 0 {
			remaining := maximum - *visited
			if opts.all {
				remaining--
			}
			nameLimit = int(max(1, remaining))
		}
		names, err = opened.Names(projectpaths.DirectoryNames{Maximum: nameLimit, IncludeHidden: opts.all || opts.almost})
		var exceeded *projectpaths.DirectoryEntriesLimitError
		if errors.As(err, &exceeded) {
			_ = opened.Close()
			return nil, false, tools.CommandLimitExceeded("traversal_visits", maximum, maximum+1)
		}
		if err != nil {
			_ = opened.Close()
		}
	}
	if err != nil {
		if reason, ok := ordinary(err); ok {
			verb, status := "open", 1
			if inv.name() == "du" {
				verb = "read"
			} else if operand {
				status = 2
			}
			_, _ = fmt.Fprintf(inv.stderr, "%s: cannot %s directory '%s': %s\n", inv.name(), verb, dir.Resolved.DisplayPath, reason)
			inv.status = max(inv.status, status)
			return nil, false, nil
		}
		return nil, false, err
	}
	defer opened.Close()
	if opts.all {
		names = append([]string{"."}, names...)
	}
	var out []lsEntry
	for _, name := range names {
		if err := inv.ctx.Err(); err != nil {
			return nil, false, err
		}
		*visited++
		inv.directoryWork.entries++
		if maximum > 0 && *visited > maximum {
			return nil, false, tools.CommandLimitExceeded("traversal_visits", maximum, *visited)
		}
		var (
			child projectpaths.BoundPath
			st    fseffect.EntryStat
		)
		if !metadata && name != "." {
			child, err = opened.Child(name)
		} else if name == "." {
			child = dir
			if metadata {
				inv.directoryWork.metadata++
				st, err = opts.stat(child)
			}
		} else {
			inv.directoryWork.metadata++
			child, st, err = opened.StatChild(name)
			if err == nil && opts.deref && typeLetter(st.Mode) == 'l' {
				if target, targetErr := opened.StatChildFollowed(child); targetErr == nil {
					st = target
				}
			}
		}
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
		if parent, err := inv.parentEntry(dir, metadata); err == nil {
			if metadata {
				inv.directoryWork.metadata++
			}
			out = append(out, parent)
		}
	}
	return out, true, nil
}
