package fseffect

import (
	"os"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/fspath"
)

// Replace stages and conditional Remove quarantines beside the destination,
// where a directory listing or watcher can see the entry. The registry holds
// each such entry by name and its held parent's identity from before it exists
// until stagingRetention after it is gone, so observers that list or report
// late can withhold exactly the entries this process created.
const stagingRetention = 2 * time.Second

type stagingEntry struct {
	directory string
	retired   time.Time
}

var staging = struct {
	sync.RWMutex
	byName map[string][]*stagingEntry
	// retired lists holds in retirement order for pruning.
	retired []stagingHold
}{byName: map[string][]*stagingEntry{}}

// stagingHold covers one entry name the door is about to create.
type stagingHold struct {
	name  string
	entry *stagingEntry
}

func holdStaging(directory, name string) stagingHold {
	entry := &stagingEntry{directory: directory}
	staging.Lock()
	staging.byName[name] = append(staging.byName[name], entry)
	staging.Unlock()
	return stagingHold{name: name, entry: entry}
}

// abandon drops a hold whose name the door never created.
func (h stagingHold) abandon() {
	staging.Lock()
	defer staging.Unlock()
	dropStagingLocked(h.name, h.entry)
}

// retire starts the retention window once the entry no longer exists.
func (h stagingHold) retire() {
	now := time.Now()
	staging.Lock()
	defer staging.Unlock()
	h.entry.retired = now
	expired := 0
	for _, old := range staging.retired {
		if now.Sub(old.entry.retired) <= stagingRetention {
			break
		}
		dropStagingLocked(old.name, old.entry)
		expired++
	}
	staging.retired = append(staging.retired[expired:], h)
}

func dropStagingLocked(name string, drop *stagingEntry) {
	entries := staging.byName[name]
	for i, entry := range entries {
		if entry == drop {
			entries = append(entries[:i], entries[i+1:]...)
			break
		}
	}
	if len(entries) == 0 {
		delete(staging.byName, name)
		return
	}
	staging.byName[name] = entries
}

// IsStaging reports whether name inside dir is, or within the retention window
// was, a replacement or removal entry this process created. It matches the
// directory by filesystem identity, so a same-named file anywhere else and any
// entry the door did not create are never reported.
func IsStaging(dir, name string) bool {
	now := time.Now()
	staging.RLock()
	var directories []string
	for _, entry := range staging.byName[name] {
		if entry.retired.IsZero() || now.Sub(entry.retired) <= stagingRetention {
			directories = append(directories, entry.directory)
		}
	}
	staging.RUnlock()
	if len(directories) == 0 {
		return false
	}
	// The trailing "." names the directory dir resolves to, not a link to it.
	identity, err := fspath.EntryIdentity(dir + string(os.PathSeparator) + ".")
	if err != nil {
		return false
	}
	for _, directory := range directories {
		if directory == identity {
			return true
		}
	}
	return false
}
