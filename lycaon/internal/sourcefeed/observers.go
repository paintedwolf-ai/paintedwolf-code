package sourcefeed

import (
	"sync"

	"github.com/lycaon/lycaon/pkg/api"
)

// Notice contains only the change categories needed by in-process projections.
type Notice struct{ FileWritesOnly bool }

func changeNotice(event api.SourceChangesEvent) Notice {
	writes := !event.Resync && !event.GitChanged && len(event.Changes) > 0
	for _, change := range event.Changes {
		writes = writes && change.Op == api.SourceChangeOpWrite && change.IsDir != nil && !*change.IsDir
	}
	return Notice{FileWritesOnly: writes}
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
