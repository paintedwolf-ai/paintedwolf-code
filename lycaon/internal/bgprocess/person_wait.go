package bgprocess

import (
	"sync"
	"time"
)

// BeginPersonWait marks a job as waiting on a person, such as an approval
// card, until the returned func runs. Overlapping waits count once.
func (r *Registry) BeginPersonWait(sessionID, handle string) func() {
	if r == nil {
		return func() {}
	}
	proc, err := r.jobs.lookup(sessionID, handle)
	if err != nil {
		return func() {}
	}
	r.jobs.mu.Lock()
	proc.personWaits++
	if proc.personWaits == 1 {
		proc.personWaitSince = time.Now().UTC()
	}
	r.jobs.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.jobs.mu.Lock()
			defer r.jobs.mu.Unlock()
			proc.personWaits--
			if proc.personWaits == 0 {
				proc.personWaited += time.Now().UTC().Sub(proc.personWaitSince)
			}
		})
	}
}

// personWaitedLocked is the job's time waiting on a person up to at.
func (p *Process) personWaitedLocked(at time.Time) time.Duration {
	waited := p.personWaited
	if p.personWaits > 0 && at.After(p.personWaitSince) {
		waited += at.Sub(p.personWaitSince)
	}
	return waited
}
