package commandprogram

import (
	"context"
	"io"
	"sync"
)

type rendezvousWaiter struct {
	activity *ExecutionWait
	unblock  func()
}

// pipeRendezvous borrows the writer's slice until it is consumed. Endpoint
// readiness and evaluator wakeups change under the same lock.
type pipeRendezvous struct {
	mu               sync.Mutex
	lifetime         context.Context
	cond             *sync.Cond
	data             []byte
	consumed         int
	closed           bool
	err              error
	readers, writers map[*rendezvousWaiter]bool
	writerActive     bool
	writerQueue      map[*rendezvousWaiter]bool
}

func newPipeRendezvous(ctx context.Context) *pipeRendezvous {
	p := &pipeRendezvous{lifetime: context.WithoutCancel(ctx), readers: map[*rendezvousWaiter]bool{}, writers: map[*rendezvousWaiter]bool{}, writerQueue: map[*rendezvousWaiter]bool{}}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func (p *pipeRendezvous) signal() { p.cond.Broadcast() }
func wakeEndpoint(w *rendezvousWaiter) {
	if w.unblock != nil {
		w.unblock()
		w.unblock = nil
	}
}
func wakeEndpoints(waiters map[*rendezvousWaiter]bool) {
	for waiter := range waiters {
		wakeEndpoint(waiter)
		delete(waiters, waiter)
	}
}

func (p *pipeRendezvous) wait(ctx context.Context, waiter *rendezvousWaiter, waiters map[*rendezvousWaiter]bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if waiter.unblock == nil {
		var err error
		waiter.unblock, err = waiter.activity.Block(ctx, WaitState{})
		if err != nil {
			return err
		}
	}
	waiters[waiter] = true
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var stop func() bool
	if ctx.Done() != nil {
		stop = context.AfterFunc(ctx, func() { p.mu.Lock(); p.cond.Broadcast(); p.mu.Unlock() })
	}
	p.cond.Wait()
	if stop != nil {
		stop()
	}
	return ctx.Err()
}

func (p *pipeRendezvous) read(ctx context.Context, b []byte, activity *ExecutionWait) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var waiter *rendezvousWaiter
	defer func() {
		if waiter != nil {
			wakeEndpoint(waiter)
			delete(p.readers, waiter)
		}
	}()
	for len(p.data) == 0 && !p.closed {
		if len(b) == 0 {
			return 0, nil
		}
		if waiter == nil {
			waiter = &rendezvousWaiter{activity: activity}
		}
		if err := p.wait(ctx, waiter, p.readers); err != nil {
			return 0, err
		}
	}
	if p.closed && p.err != nil {
		return 0, p.err
	}
	if len(p.data) > 0 {
		n := copy(b, p.data)
		p.data = p.data[n:]
		p.consumed += n
		if len(p.data) == 0 {
			wakeEndpoints(p.writers)
			p.signal()
		}
		return n, nil
	}
	return 0, io.EOF
}

func (p *pipeRendezvous) write(b []byte, activity *ExecutionWait) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0, io.ErrClosedPipe
	}
	p.data = b
	p.consumed = 0
	wakeEndpoints(p.readers)
	p.signal()
	waiter := &rendezvousWaiter{activity: activity}
	defer func() { wakeEndpoint(waiter); delete(p.writers, waiter) }()
	for len(p.data) > 0 && !p.closed {
		if err := p.wait(p.lifetime, waiter, p.writers); err != nil {
			p.closed, p.err = true, err
			p.data = nil
			wakeEndpoints(p.readers)
			p.signal()
			return p.consumed, err
		}
	}
	if p.closed && len(p.data) > 0 {
		p.data = nil
		return p.consumed, io.ErrClosedPipe
	}
	return p.consumed, nil
}

func (p *pipeRendezvous) close(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed, p.err = true, err
		wakeEndpoints(p.readers)
		wakeEndpoints(p.writers)
		p.signal()
	}
}

// acquireWriter serializes ingress accounting and completion with byte delivery.
func (p *pipeRendezvous) acquireWriter(activity *ExecutionWait) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var waiter *rendezvousWaiter
	defer func() {
		if waiter != nil {
			wakeEndpoint(waiter)
			delete(p.writerQueue, waiter)
		}
	}()
	for p.writerActive {
		if waiter == nil {
			waiter = &rendezvousWaiter{activity: activity}
		}
		if err := p.wait(p.lifetime, waiter, p.writerQueue); err != nil {
			return err
		}
	}
	p.writerActive = true
	return nil
}

func (p *pipeRendezvous) releaseWriter() {
	p.mu.Lock()
	defer p.mu.Unlock()
	wakeEndpoints(p.writerQueue)
	p.writerActive = false
	p.signal()
}
