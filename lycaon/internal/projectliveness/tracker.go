// Package projectliveness coordinates project lifecycle states (active vs. parked)
// across workspace views, active chat sessions, and running agent turns.
package projectliveness

import (
	"context"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultParkGrace debounces rapid view changes or reloads.
	DefaultParkGrace = 15 * time.Second

	// DefaultActiveWindow sets the maximum recency for warm sessions.
	DefaultActiveWindow = 1 * time.Hour

	// DefaultMaxActiveProjects bounds concurrent active projects.
	DefaultMaxActiveProjects = 8
)

// SessionActivityChecker reports active sessions in project storage.
type SessionActivityChecker interface {
	HasActiveProjectSessions(ctx context.Context, projectID string, activeSince time.Time) (bool, error)
}

// LifecycleHandler receives project activation and parking transitions.
type LifecycleHandler interface {
	OnProjectActivate(ctx context.Context, projectID string) error
	OnProjectPark(ctx context.Context, projectID string) error
}

// Config configures Tracker behavior.
type Config struct {
	ParkGrace         time.Duration
	ActiveWindow      time.Duration
	MaxActiveProjects int
	Sessions          SessionActivityChecker
	Handler           LifecycleHandler
	Now               func() time.Time
}

type projectState struct {
	workspaceClaims int
	sessions        map[string]int
	turns           map[string]int
	parked          bool
	lastActive      time.Time
	parkTimer       *time.Timer
}

// Tracker manages project active and parked states.
type Tracker struct {
	mu                sync.Mutex
	parkGrace         time.Duration
	activeWindow      time.Duration
	maxActiveProjects int
	sessions          SessionActivityChecker
	handler           LifecycleHandler
	now               func() time.Time
	projects          map[string]*projectState
	closed            bool
}

func New(cfg Config) *Tracker {
	grace := cfg.ParkGrace
	if grace <= 0 {
		grace = DefaultParkGrace
	}
	activeWindow := cfg.ActiveWindow
	if activeWindow <= 0 {
		activeWindow = DefaultActiveWindow
	}
	maxActive := cfg.MaxActiveProjects
	if maxActive <= 0 {
		maxActive = DefaultMaxActiveProjects
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Tracker{
		parkGrace:         grace,
		activeWindow:      activeWindow,
		maxActiveProjects: maxActive,
		sessions:          cfg.Sessions,
		handler:           cfg.Handler,
		now:               now,
		projects:          make(map[string]*projectState),
	}
}

// ClaimWorkspace marks a project open in a workspace view.
func (t *Tracker) ClaimWorkspace(projectID string) func() {
	return t.claim(projectID, func(p *projectState) {
		p.workspaceClaims++
	}, func(p *projectState) {
		if p.workspaceClaims > 0 {
			p.workspaceClaims--
		}
	})
}

// ClaimSession tracks an active chat session in the project.
func (t *Tracker) ClaimSession(projectID, sessionID string) func() {
	sessionID = strings.TrimSpace(sessionID)
	return t.claim(projectID, func(p *projectState) {
		if sessionID != "" {
			p.sessions[sessionID]++
		}
	}, func(p *projectState) {
		if sessionID != "" {
			if count := p.sessions[sessionID] - 1; count > 0 {
				p.sessions[sessionID] = count
			} else {
				delete(p.sessions, sessionID)
			}
		}
	})
}

// ClaimTurn tracks an executing agent turn in the project.
func (t *Tracker) ClaimTurn(projectID, turnID string) func() {
	turnID = strings.TrimSpace(turnID)
	return t.claim(projectID, func(p *projectState) {
		if turnID != "" {
			p.turns[turnID]++
		}
	}, func(p *projectState) {
		if turnID != "" {
			if count := p.turns[turnID] - 1; count > 0 {
				p.turns[turnID] = count
			} else {
				delete(p.turns, turnID)
			}
		}
	})
}

func (t *Tracker) claim(projectID string, onClaim, onRelease func(*projectState)) func() {
	projectID = strings.TrimSpace(projectID)
	if t == nil || projectID == "" {
		return func() {}
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return func() {}
	}
	p := t.getOrCreateLocked(projectID)
	wasParked := p.parked
	if p.parkTimer != nil {
		p.parkTimer.Stop()
		p.parkTimer = nil
	}
	p.parked = false
	p.lastActive = t.now()
	onClaim(p)

	t.evictOldestActiveLocked(projectID)
	t.mu.Unlock()

	if wasParked && t.handler != nil {
		_ = t.handler.OnProjectActivate(context.Background(), projectID)
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			if t.closed {
				return
			}
			p, ok := t.projects[projectID]
			if !ok {
				return
			}
			onRelease(p)
			p.lastActive = t.now()
			t.evaluateProjectLocked(context.Background(), projectID, p)
		})
	}
}

func (t *Tracker) getOrCreateLocked(projectID string) *projectState {
	p := t.projects[projectID]
	if p == nil {
		p = &projectState{
			sessions: make(map[string]int),
			turns:    make(map[string]int),
		}
		t.projects[projectID] = p
	}
	return p
}

// IsParked reports whether a project is currently parked.
func (t *Tracker) IsParked(projectID string) bool {
	if t == nil {
		return false
	}
	projectID = strings.TrimSpace(projectID)
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.projects[projectID]
	if !ok {
		return false
	}
	return p.parked
}

// ParkNow immediately parks a project if it has no active workspace or turns.
func (t *Tracker) ParkNow(ctx context.Context, projectID string) bool {
	if t == nil {
		return false
	}
	projectID = strings.TrimSpace(projectID)
	t.mu.Lock()
	p, ok := t.projects[projectID]
	if !ok || p.parked {
		t.mu.Unlock()
		return false
	}
	if p.workspaceClaims > 0 || len(p.turns) > 0 {
		t.mu.Unlock()
		return false
	}
	if p.parkTimer != nil {
		p.parkTimer.Stop()
		p.parkTimer = nil
	}
	p.parked = true
	t.mu.Unlock()

	if t.handler != nil {
		_ = t.handler.OnProjectPark(ctx, projectID)
	}
	return true
}

func (t *Tracker) evaluateProjectLocked(ctx context.Context, projectID string, p *projectState) {
	if p.parked || p.workspaceClaims > 0 || len(p.turns) > 0 || len(p.sessions) > 0 {
		if p.parkTimer != nil {
			p.parkTimer.Stop()
			p.parkTimer = nil
		}
		return
	}

	if t.sessions != nil {
		activeSince := t.now().Add(-t.activeWindow)
		hasActive, err := t.sessions.HasActiveProjectSessions(ctx, projectID, activeSince)
		if err == nil && hasActive {
			if p.parkTimer != nil {
				p.parkTimer.Stop()
				p.parkTimer = nil
			}
			return
		}
	}

	if p.parkTimer != nil {
		return
	}

	p.parkTimer = time.AfterFunc(t.parkGrace, func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.closed {
			return
		}
		cur, ok := t.projects[projectID]
		if !ok || cur != p || cur.parked {
			return
		}
		if cur.workspaceClaims > 0 || len(cur.turns) > 0 || len(cur.sessions) > 0 {
			cur.parkTimer = nil
			return
		}
		if t.sessions != nil {
			activeSince := t.now().Add(-t.activeWindow)
			hasActive, err := t.sessions.HasActiveProjectSessions(context.Background(), projectID, activeSince)
			if err == nil && hasActive {
				cur.parkTimer = nil
				return
			}
		}
		cur.parked = true
		cur.parkTimer = nil
		if t.handler != nil {
			go func(id string) {
				_ = t.handler.OnProjectPark(context.Background(), id)
			}(projectID)
		}
	})
}

func (t *Tracker) evictOldestActiveLocked(currentProjectID string) {
	if t.maxActiveProjects <= 0 {
		return
	}
	activeCount := 0
	for _, p := range t.projects {
		if !p.parked {
			activeCount++
		}
	}
	if activeCount <= t.maxActiveProjects {
		return
	}

	var oldestID string
	var oldestTime time.Time
	for id, p := range t.projects {
		if id == currentProjectID || p.parked || p.workspaceClaims > 0 || len(p.turns) > 0 {
			continue
		}
		if oldestID == "" || p.lastActive.Before(oldestTime) {
			oldestID = id
			oldestTime = p.lastActive
		}
	}
	if oldestID == "" {
		return
	}
	oldest := t.projects[oldestID]
	if oldest.parkTimer != nil {
		oldest.parkTimer.Stop()
		oldest.parkTimer = nil
	}
	oldest.parked = true
	if t.handler != nil {
		go func(id string) {
			_ = t.handler.OnProjectPark(context.Background(), id)
		}(oldestID)
	}
}

// Close cancels pending park timers and cleans up.
func (t *Tracker) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	for _, p := range t.projects {
		if p.parkTimer != nil {
			p.parkTimer.Stop()
			p.parkTimer = nil
		}
	}
}
