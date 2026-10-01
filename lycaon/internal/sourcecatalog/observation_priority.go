package sourcecatalog

import (
	"sync"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/repochange"
)

type observationInterests struct {
	mu     sync.Mutex
	groups map[string]*backgroundwork.PriorityGroup
}

func (i *observationInterests) join(dir string, priority backgroundwork.Priority) func() {
	i.mu.Lock()
	if i.groups == nil {
		i.groups = make(map[string]*backgroundwork.PriorityGroup)
	}
	group := i.groups[dir]
	if group == nil {
		group = &backgroundwork.PriorityGroup{}
		i.groups[dir] = group
	}
	release := group.Add(priority)
	i.mu.Unlock()
	return func() {
		i.mu.Lock()
		defer i.mu.Unlock()
		release()
		if group.Empty() && i.groups[dir] == group {
			delete(i.groups, dir)
		}
	}
}
func (s *indexStore) observationRequest(dir string) backgroundwork.Request {
	s.observationInterests.mu.Lock()
	group := s.observationInterests.groups[dir]
	s.observationInterests.mu.Unlock()
	return backgroundwork.Request{Key: s.workKey() + ":directory:" + dir, Epoch: repochange.CurrentEpoch(s.root.Path).Value,
		Lane: s.root.Path, Priority: backgroundwork.PriorityProactive, Interests: group, Resources: []backgroundwork.Resource{backgroundwork.ResourceDirectory}}
}
