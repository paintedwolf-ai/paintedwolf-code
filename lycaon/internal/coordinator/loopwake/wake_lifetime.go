package loopwake

import "sync"

// Retirement waits for admissions that can still enqueue work or start a drain.
type wakeLifetimes struct {
	mu       sync.Mutex
	sessions map[string]*wakeLifetime
}
type wakeLifetime struct {
	accepting sync.WaitGroup
	closed    chan struct{}
}

func (w *wakeLifetimes) session(id string) *wakeLifetime {
	if w.sessions == nil {
		w.sessions = make(map[string]*wakeLifetime)
	}
	life := w.sessions[id]
	if life == nil {
		life = &wakeLifetime{}
		w.sessions[id] = life
	}
	return life
}

func (w *wakeLifetimes) enter(id string) (func(), bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	life := w.session(id)
	if life.closed != nil {
		return nil, false
	}
	life.accepting.Add(1)
	return life.accepting.Done, true
}

func (w *wakeLifetimes) retire(id string) (func(), bool) {
	w.mu.Lock()
	life := w.session(id)
	if done := life.closed; done != nil {
		w.mu.Unlock()
		<-done
		return nil, false
	}
	life.closed = make(chan struct{})
	w.mu.Unlock()
	life.accepting.Wait()
	return func() {
		w.mu.Lock()
		delete(w.sessions, id)
		close(life.closed)
		w.mu.Unlock()
	}, true
}
