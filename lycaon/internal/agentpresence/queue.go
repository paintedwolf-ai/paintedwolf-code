package agentpresence

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/pkg/api"
)

// maxQueuedDocumentChecks bounds pending staleness checks; lifecycle facts are never dropped.
const maxQueuedDocumentChecks = 4096

// workQueue applies facts in the order they were observed, off the calling path.
// Turn boundaries and tool facts share it, so a turn that starts is always
// applied before the calls it runs.
type workQueue struct {
	mu      sync.Mutex
	idle    *sync.Cond
	jobs    []func()
	running bool
	// documents coalesces checks per document to the newest revision.
	documents map[string]int64
}

func (t *Tracker) enqueue(job func()) {
	q := &t.queue
	q.mu.Lock()
	if q.idle == nil {
		q.idle = sync.NewCond(&q.mu)
	}
	q.jobs = append(q.jobs, job)
	if q.running {
		q.mu.Unlock()
		return
	}
	q.running = true
	q.mu.Unlock()
	go t.drain()
}

func (t *Tracker) drain() {
	q := &t.queue
	for {
		q.mu.Lock()
		if len(q.jobs) == 0 {
			q.running = false
			q.idle.Broadcast()
			q.mu.Unlock()
			return
		}
		job := q.jobs[0]
		q.jobs[0] = nil
		q.jobs = q.jobs[1:]
		q.mu.Unlock()
		job()
	}
}

// Settle waits until every observed fact has been applied.
func (t *Tracker) Settle() {
	q := &t.queue
	q.mu.Lock()
	defer q.mu.Unlock()
	for q.running {
		q.idle.Wait()
	}
}

// SetAnchors installs the editor document anchoring port once documents exist.
func (t *Tracker) SetAnchors(anchors Anchors) {
	t.enqueue(func() { t.anchors = anchors })
}

// ObserveSession follows chat turns and titles from session lifecycle events.
func (t *Tracker) ObserveSession(ctx context.Context, ev api.SessionEvent) {
	if t == nil || ev.ID == "" {
		return
	}
	ctx = context.WithoutCancel(ctx)
	switch {
	case ev.Status == api.SessionStatusBusy:
		t.enqueue(func() { t.turnStarted(ctx, ev.ID, ev.CurrentTurn) })
	case ev.Status == api.SessionStatusIdle && (ev.IdleDisposition != "" || ev.HostError != nil):
		t.enqueue(func() { t.turnEnded(ctx, ev.ID) })
	}
	if ev.Title != "" {
		t.enqueue(func() { t.sessionRetitled(ctx, ev.ID, ev.Title) })
	}
}

// CallStarted records a running call with one file target.
func (t *Tracker) CallStarted(ctx context.Context, call Call, target Target, kind api.AgentActivityKind) {
	if t == nil {
		return
	}
	ctx = context.WithoutCancel(ctx)
	t.enqueue(func() { t.callStarted(ctx, call, target, kind) })
}

// CallEnded removes a finished call's activity and unlanded intents.
func (t *Tracker) CallEnded(ctx context.Context, call Call) {
	if t == nil {
		return
	}
	ctx = context.WithoutCancel(ctx)
	t.enqueue(func() { t.callEnded(ctx, call) })
}

// ReadsReturned records text a call returned to the model.
func (t *Tracker) ReadsReturned(ctx context.Context, call Call, reads []Read) {
	if t == nil || len(reads) == 0 {
		return
	}
	ctx = context.WithoutCancel(ctx)
	t.enqueue(func() { t.readsReturned(ctx, call, reads) })
}

// IntentsResolved records a call's resolved, unlanded mutations.
func (t *Tracker) IntentsResolved(ctx context.Context, call Call, intents []Intent) {
	if t == nil {
		return
	}
	ctx = context.WithoutCancel(ctx)
	t.enqueue(func() { t.intentsResolved(ctx, call, intents) })
}

// IntentsAwaitingApproval marks a call's intents as held by a checkpoint.
func (t *Tracker) IntentsAwaitingApproval(ctx context.Context, call Call, checkpointID string) {
	if t == nil {
		return
	}
	ctx = context.WithoutCancel(ctx)
	t.enqueue(func() { t.intentsAwaitingApproval(ctx, call, checkpointID) })
}

// IntentsApproved returns a call's held intents to pending.
func (t *Tracker) IntentsApproved(ctx context.Context, call Call) {
	if t == nil {
		return
	}
	ctx = context.WithoutCancel(ctx)
	t.enqueue(func() { t.intentsApproved(ctx, call) })
}

// IntentsLanded removes a call's intents once its mutation committed.
func (t *Tracker) IntentsLanded(ctx context.Context, call Call, documents []Document) {
	if t == nil {
		return
	}
	ctx = context.WithoutCancel(ctx)
	t.enqueue(func() { t.intentsLanded(ctx, call, documents) })
}

// DocumentChanged schedules a staleness check after a document's content
// changed. Checks for one document coalesce to its newest revision.
func (t *Tracker) DocumentChanged(ctx context.Context, projectID, documentID string, revision int64) {
	if t == nil || documentID == "" {
		return
	}
	ctx = context.WithoutCancel(ctx)
	key := projectID + "\x00" + documentID
	q := &t.queue
	q.mu.Lock()
	if q.documents == nil {
		q.documents = make(map[string]int64)
	}
	pending, queued := q.documents[key]
	if queued || len(q.documents) >= maxQueuedDocumentChecks {
		if queued && revision > pending {
			q.documents[key] = revision
		}
		q.mu.Unlock()
		return
	}
	q.documents[key] = revision
	q.mu.Unlock()
	t.enqueue(func() {
		q.mu.Lock()
		latest := q.documents[key]
		delete(q.documents, key)
		q.mu.Unlock()
		t.documentChanged(ctx, projectID, documentID, latest)
	})
}
