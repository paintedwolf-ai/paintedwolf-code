package nfsserver

import (
	"container/list"
	"sync"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// listingIndex keeps the listings that rebuilt evicted handles. A walk larger than the handle table
// evicts as it goes, and without this each rebuilt file lists its whole directory again. An answer
// is only a candidate: the caller confirms the identity at that name before using it.
type listingIndex struct {
	mu         sync.Mutex
	maxEntries int
	entries    int
	byParent   map[handleKey]*list.Element
	lru        *list.List
}

type listing struct {
	parent handleKey
	names  map[layeredfs.NodeID]string
}

func newListingIndex(maxEntries int) *listingIndex {
	return &listingIndex{maxEntries: maxEntries, byParent: make(map[handleKey]*list.Element), lru: list.New()}
}

func (l *listingIndex) name(parent handleKey, id layeredfs.NodeID) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	elem, ok := l.byParent[parent]
	if !ok {
		return "", false
	}
	l.lru.MoveToFront(elem)
	name, ok := elem.Value.(*listing).names[id]
	return name, ok
}

func (l *listingIndex) put(parent handleKey, names map[layeredfs.NodeID]string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if elem, ok := l.byParent[parent]; ok {
		l.removeLocked(elem)
	}
	l.byParent[parent] = l.lru.PushFront(&listing{parent: parent, names: names})
	l.entries += len(names)
	for l.entries > l.maxEntries && l.lru.Len() > 1 {
		l.removeLocked(l.lru.Back())
	}
}

func (l *listingIndex) dropView(viewHash uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for key, elem := range l.byParent {
		if key.viewHash == viewHash {
			l.removeLocked(elem)
		}
	}
}

func (l *listingIndex) removeLocked(elem *list.Element) {
	entry := elem.Value.(*listing)
	l.lru.Remove(elem)
	delete(l.byParent, entry.parent)
	l.entries -= len(entry.names)
}
