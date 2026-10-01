package sourcetree

import (
	"context"
	"path"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
)

type liveView struct {
	wake        chan struct{}
	directories map[Address]time.Time
	dirty       map[Address]struct{}
	unsubscribe []func()
}

func (v *View) startLive() {
	v.live = liveView{wake: make(chan struct{}, 1), directories: make(map[Address]time.Time), dirty: make(map[Address]struct{})}
	for _, root := range v.rootOrder {
		v.live.directories[Address{Root: root.ID, Path: "."}] = time.Now()
		v.live.unsubscribe = append(v.live.unsubscribe, v.catalog.SubscribeNavigation(root.Root, v.wakeLive))
	}
	v.live.unsubscribe = append(v.live.unsubscribe, repochange.RegisterObserver(v.repositoryChanged))
	v.workers.Add(1)
	go v.runLive()
}
func (v *View) wakeLive() {
	select {
	case v.live.wake <- struct{}{}:
	default:
	}
}
func (v *View) repositoryChanged(_ context.Context, event repochange.Event) {
	if event.Kind == repochange.IndexChanged {
		return
	}
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return
	}
	for address, seen := range v.live.directories {
		root := v.roots[address.Root]
		if root.Path != event.ProjectDir || address.Path != "." && time.Since(seen) > time.Minute {
			continue
		}
		affected := len(event.Paths) == 0
		for _, changed := range event.Paths {
			if path.Dir(changed) == address.Path || under(address.Path, changed) {
				affected = true
				break
			}
		}
		if affected {
			v.live.dirty[address] = struct{}{}
		}
	}
	v.mu.Unlock()
	v.wakeLive()
}
func (v *View) runLive() {
	defer v.workers.Done()
	defer func() {
		for _, unsubscribe := range v.live.unsubscribe {
			unsubscribe()
		}
	}()
	for {
		select {
		case <-v.ctx.Done():
			return
		case <-v.live.wake:
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-v.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		v.mu.Lock()
		if v.closed {
			v.mu.Unlock()
			return
		}
		for address := range v.live.dirty {
			if len(v.loading) >= 8 {
				break
			}
			v.loadLocked(v.roots[address.Root], address.Path)
			delete(v.live.dirty, address)
		}
		if len(v.live.dirty) > 0 {
			v.wakeLive()
		}
		v.mu.Unlock()
		v.refreshBoundaries(v.ctx)
		if v.notify != nil {
			v.notify()
		}
	}
}
