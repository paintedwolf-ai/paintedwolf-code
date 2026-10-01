package llm

import (
	"context"
	"sync"
	"time"
)

// OverlayLaneWait is how long an overlay call waits for a single-flight
// instance before skipping. Gift and quality wait on the parent context.
const OverlayLaneWait = 10 * time.Second

type utilityLanes struct {
	mu    sync.Mutex
	lanes map[string]*singleLane
	// overlayWait bounds an overlay call's wait for the instance. Zero means
	// OverlayLaneWait; a test narrows it to reach the give-up path quickly.
	overlayWait time.Duration
}

// overlayWaitOrDefault returns the bound an overlay call waits under.
func (l *utilityLanes) overlayWaitOrDefault() time.Duration {
	if l != nil && l.overlayWait > 0 {
		return l.overlayWait
	}
	return OverlayLaneWait
}

type singleLane struct {
	ch        chan struct{}
	coord     int
	coordWait chan struct{}
}

func newUtilityLanes() *utilityLanes {
	return &utilityLanes{lanes: make(map[string]*singleLane)}
}

func (l *utilityLanes) acquire(ctx context.Context, providerID string, singleFlight bool, class UtilityClass) (func(), error) {
	if l == nil || !singleFlight || providerID == "" {
		return func() {}, nil
	}
	lane := l.lane(providerID)
	wait := ctx
	cancel := func() {}
	if class == UtilityClassOverlay {
		wait, cancel = context.WithTimeout(ctx, l.overlayWaitOrDefault())
	}
	if err := l.waitForCoordinator(wait, lane, class); err != nil {
		cancel()
		return nil, err
	}
	select {
	case lane.ch <- struct{}{}:
		cancel()
		return func() { <-lane.ch }, nil
	case <-wait.Done():
		cancel()
		if class == UtilityClassOverlay {
			return nil, ErrLiteBusy
		}
		return nil, wait.Err()
	}
}

func (l *utilityLanes) holdCoordinator(providerID string) func() {
	if l == nil || providerID == "" {
		return func() {}
	}
	lane := l.lane(providerID)
	l.mu.Lock()
	lane.coord++
	l.mu.Unlock()
	return func() {
		l.mu.Lock()
		lane.coord--
		if lane.coord <= 0 {
			lane.coord = 0
			if lane.coordWait != nil {
				close(lane.coordWait)
				lane.coordWait = nil
			}
		}
		l.mu.Unlock()
	}
}

func (l *utilityLanes) waitForCoordinator(wait context.Context, lane *singleLane, class UtilityClass) error {
	for {
		l.mu.Lock()
		if lane.coord == 0 {
			l.mu.Unlock()
			return nil
		}
		ch := lane.coordClosedLocked()
		l.mu.Unlock()
		select {
		case <-ch:
		case <-wait.Done():
			if class == UtilityClassOverlay {
				return ErrLiteBusy
			}
			return wait.Err()
		}
	}
}

func (lane *singleLane) coordClosedLocked() <-chan struct{} {
	if lane.coordWait == nil {
		lane.coordWait = make(chan struct{})
	}
	return lane.coordWait
}

func (l *utilityLanes) lane(id string) *singleLane {
	l.mu.Lock()
	defer l.mu.Unlock()
	if existing, ok := l.lanes[id]; ok {
		return existing
	}
	lane := &singleLane{ch: make(chan struct{}, 1)}
	l.lanes[id] = lane
	return lane
}
