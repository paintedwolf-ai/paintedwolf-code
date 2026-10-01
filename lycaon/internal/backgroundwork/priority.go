package backgroundwork

import "sync"

// PriorityGroup lets joined consumers promote a shared queued task. Removing
// the last interactive interest restores background priority before admission.
type PriorityGroup struct {
	mu     sync.Mutex
	counts [3]int
}

func (g *PriorityGroup) Add(priority Priority) func() {
	g.mu.Lock()
	g.counts[priority]++
	g.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { g.mu.Lock(); g.counts[priority]--; g.mu.Unlock() }) }
}
func (g *PriorityGroup) Priority(fallback Priority) Priority {
	g.mu.Lock()
	defer g.mu.Unlock()
	for priority, count := range g.counts {
		if count > 0 {
			return Priority(priority)
		}
	}
	return fallback
}
func (g *PriorityGroup) Empty() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.counts == [3]int{}
}
