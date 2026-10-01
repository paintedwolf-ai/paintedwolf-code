package sourcetree

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

type retainedReview struct {
	key     string
	overlay *sourcecatalog.TreeOverlay
	refs    int
}
type reviewBuild struct{ cancel context.CancelFunc }
type reviewFailure struct {
	key string
	err error
}

// ReviewSet retains sparse facts and projections; frame references delay projection release.
type ReviewSet struct {
	fingerprint pagedview.Fingerprint
	mu          sync.Mutex
	workers     sync.WaitGroup
	closed      bool
	failures    map[string]reviewFailure
	once        sync.Once
	refs        int
	facts       map[string]*sourcecatalog.TreeOverlay
	current     map[string]*retainedReview
	building    map[string]*reviewBuild
}

func newReviewSet(facts map[string]*sourcecatalog.TreeOverlay, fingerprint pagedview.Fingerprint) *ReviewSet {
	return &ReviewSet{fingerprint: fingerprint, refs: 1, facts: facts, current: make(map[string]*retainedReview), building: make(map[string]*reviewBuild), failures: make(map[string]reviewFailure)}
}
func (r *ReviewSet) retain() func() {
	r.mu.Lock()
	r.refs++
	r.mu.Unlock()
	var once sync.Once
	return func() { once.Do(r.release) }
}
func (r *ReviewSet) Close() {
	r.once.Do(func() {
		r.mu.Lock()
		r.closed = true
		for _, work := range r.building {
			work.cancel()
		}
		r.mu.Unlock()
		r.workers.Wait()
		r.release()
	})
}
func (r *ReviewSet) release() {
	r.mu.Lock()
	r.refs--
	if r.refs > 0 {
		r.mu.Unlock()
		return
	}
	facts, current := r.facts, r.current
	r.facts = nil
	r.current = nil
	r.mu.Unlock()
	for _, entry := range current {
		r.releaseProjection(entry)
	}
	for _, overlay := range facts {
		disposeReviewOverlay(overlay)
	}
}

//nolint:contextcheck,nolintlint // Final-reference cleanup runs after preparation cancellation.
func disposeReviewOverlay(overlay *sourcecatalog.TreeOverlay) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = overlay.Release(ctx)
	overlay.Close()
}
func (r *ReviewSet) releaseProjection(entry *retainedReview) {
	r.mu.Lock()
	entry.refs--
	dispose := entry.refs == 0
	r.mu.Unlock()
	if dispose {
		disposeReviewOverlay(entry.overlay)
	}
}

// projection only acquires an immutable publication. Missing weighted overlays
// are prepared independently of the requesting frame and progress summary. A
// build owns the source's generation pin; every other outcome releases it here.
func (r *ReviewSet) projection(lifetime context.Context, source reviewSource, intent string, notify func()) (*ReviewProjection, func(), error) {
	key := intent + ":" + source.revision
	r.mu.Lock()
	defer r.mu.Unlock()
	building := false
	defer func() {
		if !building && source.pin != nil {
			source.pin.Release()
		}
	}()
	if r.closed {
		// The view is still retained; only this captured review was superseded.
		return nil, nil, pagedview.ErrRevision
	}
	if r.facts[source.root] == nil {
		return nil, nil, nil
	}
	if entry := r.current[source.root]; entry != nil && entry.key == key {
		entry.refs++
		return NewReviewProjection(entry.overlay), func() { r.releaseProjection(entry) }, nil
	}
	if failure, ok := r.failures[source.root]; ok && failure.key == key {
		return nil, nil, failure.err
	}
	if r.building[source.root] == nil {
		building = true
		ctx, cancel := context.WithCancel(lifetime)
		r.building[source.root] = &reviewBuild{cancel: cancel}
		r.workers.Add(1)
		go func() {
			defer r.workers.Done()
			defer cancel()
			if source.pin != nil {
				defer source.pin.Release()
			}
			r.buildProjection(ctx, source, key)
			if notify != nil {
				notify()
			}
		}()
	}
	return nil, nil, pagedview.ErrPreparing
}

func (r *ReviewSet) hasFacts(root string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.facts[root] != nil
}

func (r *ReviewSet) buildProjection(ctx context.Context, source reviewSource, key string) {
	overlay, err := r.facts[source.root].Fork(ctx)
	if err == nil {
		err = prepareReview(ctx, source, overlay)
	}
	r.mu.Lock()
	delete(r.building, source.root)
	if err == nil && r.closed {
		err = pagedview.ErrExpired
	}
	if err != nil {
		if !errors.Is(err, pagedview.ErrRevision) && !errors.Is(err, context.Canceled) {
			r.failures[source.root] = reviewFailure{key, err}
		}
		r.mu.Unlock()
		if overlay != nil {
			disposeReviewOverlay(overlay)
		}
		return
	}
	previous := r.current[source.root]
	r.current[source.root] = &retainedReview{key: key, overlay: overlay, refs: 1}
	delete(r.failures, source.root)
	r.mu.Unlock()
	if previous != nil {
		r.releaseProjection(previous)
	}
}

// SetReview installs prepared facts atomically with respect to new snapshots.
func (v *View) SetReview(review *ReviewSet) error {
	v.mu.Lock()
	if v.closed || v.ctx.Err() != nil {
		v.mu.Unlock()
		return pagedview.ErrExpired
	}
	previous := v.review
	if previous == review {
		v.mu.Unlock()
		return nil
	}
	if previous != nil && review != nil && previous.fingerprint == review.fingerprint {
		v.mu.Unlock()
		review.Close()
		return nil
	}
	v.review = review
	v.revision = uuid.NewString()
	v.resetPreparationLocked()
	v.mu.Unlock()
	if previous != nil {
		previous.Close()
	}
	if v.notify != nil {
		v.notify()
	}
	return nil
}
