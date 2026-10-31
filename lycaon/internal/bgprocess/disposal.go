package bgprocess

import (
	"context"
)

// sessionDrain retains the physical jobs after their public handles disappear.
// It exists only until those exact jobs finish exiting and draining output.
type sessionDrain struct {
	procs map[string]*Process
	done  chan struct{}
}

func (r *processTable) beginDrainLocked(sessionID string) *sessionDrain {
	if drain := r.drains[sessionID]; drain != nil {
		return drain
	}
	procs := r.sessions[sessionID]
	if len(procs) == 0 {
		delete(r.sessions, sessionID)
		return nil
	}
	delete(r.sessions, sessionID)
	for handle, proc := range procs {
		r.unindexProjectProcessLocked(proc)
		delete(r.workDirs, handle)
		proc.discarded = true
	}
	drain := &sessionDrain{procs: procs, done: make(chan struct{})}
	if r.drains == nil {
		r.drains = make(map[string]*sessionDrain)
	}
	r.drains[sessionID] = drain
	go r.finishDrain(sessionID, drain)
	return drain
}

func (r *processTable) finishDrain(sessionID string, drain *sessionDrain) {
	for _, proc := range drain.procs {
		<-proc.done
	}
	r.mu.Lock()
	owned := make([]completionResources, 0, len(drain.procs))
	for _, proc := range drain.procs {
		owned = append(owned, proc.resources.take())
	}
	r.mu.Unlock()
	for _, resources := range owned {
		resources.release()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.drains[sessionID] == drain {
		delete(r.drains, sessionID)
	}
	close(drain.done)
}

// ownedProcessesLocked includes hidden jobs still holding their physical lifetime.
// Admission prevents a new public session map while its prior drain is pending.
func (r *processTable) ownedProcessesLocked(sessionID string) map[string]*Process {
	if drain := r.drains[sessionID]; drain != nil {
		return drain.procs
	}
	return r.sessions[sessionID]
}

func (proc *Process) pendingDrainLocked() bool {
	if !proc.discarded {
		return false
	}
	select {
	case <-proc.done:
		return false
	default:
		return true
	}
}

// DisposeSession hides handles, closes admission and joins their physical drain.
// A canceled wait leaves the same drain owned for retry until every job settles.
func (r *ProcessLifecycle) DisposeSession(ctx context.Context, sessionID string, reason TerminationReason) error {
	if r == nil {
		return nil
	}
	r.jobs.mu.Lock()
	drain := r.jobs.beginDrainLocked(trim(sessionID))
	r.jobs.mu.Unlock()
	if drain == nil {
		return nil
	}
	if reason == "" {
		reason = TerminationReasonFromContext(ctx, TerminationChatDeleted)
	}
	err := r.settleDrains(ctx, []*sessionDrain{drain}, reason)
	r.sweeper.onJobEnded()
	return err
}

// Close permanently closes admission; every retry joins any remaining drains.
func (r *ProcessLifecycle) Close(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, processCloseTimeout)
	defer cancel()
	if r == nil {
		return nil
	}
	r.jobs.mu.Lock()
	r.jobs.closed = true
	for sessionID := range r.jobs.sessions {
		r.jobs.beginDrainLocked(sessionID)
	}
	drains := make([]*sessionDrain, 0, len(r.jobs.drains))
	for _, drain := range r.jobs.drains {
		drains = append(drains, drain)
	}
	r.jobs.mu.Unlock()
	r.sweeper.close()
	return r.settleDrains(ctx, drains, TerminationAppQuit)
}

func (r *ProcessLifecycle) settleDrains(ctx context.Context, drains []*sessionDrain, reason TerminationReason) error {
	for _, drain := range drains {
		for _, proc := range drain.procs {
			r.killProcess(proc, reason)
		}
	}
	for _, drain := range drains {
		select {
		case <-drain.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// failStart settles an admitted job that never acquired a child. A concurrent
// teardown may already have hidden it, so it still owes physical completion.
func (r *processTable) failStart(proc *Process) {
	r.mu.Lock()
	proc.running, proc.hasExit, proc.exitCode = false, true, -1
	close(proc.done)
	r.mu.Unlock()
	if proc.screen != nil {
		proc.screen.Close()
	}
	r.remove(proc.SessionID, proc.Handle)
}
