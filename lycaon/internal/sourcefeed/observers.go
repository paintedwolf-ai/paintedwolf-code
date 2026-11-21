package sourcefeed

import (
	"sync"

	"github.com/lycaon/lycaon/pkg/api"
)

// Notice contains only the change categories and addresses needed by
// in-process projections.
type Notice struct {
	FileWritesOnly bool
	// Changed names each changed root and path, and the source of a move.
	Changed []Address
}

// Address is one root and root-relative path.
type Address struct{ RootID, Path string }

func changeNotice(event api.SourceChangesEvent) Notice {
	writes := !event.Resync && !event.GitChanged && len(event.Changes) > 0
	changed := make([]Address, 0, len(event.Changes))
	for _, change := range event.Changes {
		writes = writes && change.Op == api.SourceChangeOpWrite && change.IsDir != nil && !*change.IsDir
		changed = append(changed, Address{RootID: change.RootID, Path: change.Path})
		if change.FromPath != "" {
			root := change.FromRootID
			if root == "" {
				root = change.RootID
			}
			changed = append(changed, Address{RootID: root, Path: change.FromPath})
		}
	}
	return Notice{FileWritesOnly: writes, Changed: changed}
}

// Source observers receive a content-free signal only after a committed change.
var sourceObservers struct {
	sync.Mutex
	next    uint64
	entries map[uint64]sourceObserver
}

type sourceObserver struct {
	project, workspace string
	changed            func(Notice)
}

func Subscribe(project, workspace string, changed func(Notice)) func() {
	sourceObservers.Lock()
	if sourceObservers.entries == nil {
		sourceObservers.entries = make(map[uint64]sourceObserver)
	}
	sourceObservers.next++
	id := sourceObservers.next
	sourceObservers.entries[id] = sourceObserver{project, workspace, changed}
	sourceObservers.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() { sourceObservers.Lock(); delete(sourceObservers.entries, id); sourceObservers.Unlock() })
	}
}
func notifySourceObservers(project, workspace string, notice Notice) {
	sourceObservers.Lock()
	var listeners []func(Notice)
	for _, observer := range sourceObservers.entries {
		if observer.project == project && observer.workspace == workspace {
			listeners = append(listeners, observer.changed)
		}
	}
	sourceObservers.Unlock()
	for _, changed := range listeners {
		changed(notice)
	}
}
