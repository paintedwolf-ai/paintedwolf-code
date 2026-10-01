package pagesession

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/browser"
)

const (
	DefaultMaxPages    = 2
	DefaultIdleTimeout = 10 * time.Minute
	reapInterval       = 30 * time.Second
)

var (
	ErrPageCapReached = errors.New("page cap reached")
	ErrPageNotFound   = errors.New("page not found")
	ErrPageNotRunning = errors.New("page is not running")
)

// Config tunes per-session held-page limits.
type Config struct {
	MaxPages    int
	IdleTimeout time.Duration
}

// DefaultConfig returns the production page-session defaults.
func DefaultConfig() Config {
	return Config{
		MaxPages:    DefaultMaxPages,
		IdleTimeout: DefaultIdleTimeout,
	}
}

// Entry is one held page in the registry.
type Entry struct {
	ID        string
	SessionID string
	Held      *browser.HeldPage
	TargetURL string
	OpenedAt  time.Time
	LastUsed  time.Time
}

// Registry tracks session-scoped live browser pages (cookies/auth persist across acts).
type Registry struct {
	mu       sync.Mutex
	sessions map[string]map[string]*Entry
	cfg      Config
	stop     chan struct{}
	done     chan struct{}
	onClose  func(sessionID, pageID string)
}

// NewRegistry constructs an empty page registry and starts the idle reaper.
func NewRegistry(cfg Config) *Registry {
	if cfg.MaxPages <= 0 {
		cfg.MaxPages = DefaultMaxPages
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = DefaultIdleTimeout
	}
	r := &Registry{
		sessions: make(map[string]map[string]*Entry),
		cfg:      cfg,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go r.reapLoop()
	return r
}

// SetOnClose registers a callback invoked after a page is removed (close/reap/abort).
func (r *Registry) SetOnClose(fn func(sessionID, pageID string)) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.onClose = fn
	r.mu.Unlock()
}

// Close stops the idle reaper and tears down every held page.
func (r *Registry) Close(ctx context.Context) {
	if r == nil {
		return
	}
	select {
	case <-r.stop:
	default:
		close(r.stop)
	}
	<-r.done
	type closed struct{ sid, id string }
	var notify []closed
	r.mu.Lock()
	for sid, pages := range r.sessions {
		for id, e := range pages {
			_ = e.Held.Close(ctx)
			notify = append(notify, closed{sid, id})
			delete(pages, id)
		}
		delete(r.sessions, sid)
	}
	r.mu.Unlock()
	for _, n := range notify {
		r.fireClose(n.sid, n.id)
	}
}

// Open registers an already-open held page under sessionID.
func (r *Registry) Open(ctx context.Context, sessionID string, held *browser.HeldPage) (*Entry, error) {
	if r == nil {
		return nil, fmt.Errorf("page registry not configured")
	}
	if held == nil || held.Page == nil {
		return nil, fmt.Errorf("held page required")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session id required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	pages := r.sessions[sessionID]
	if pages == nil {
		pages = make(map[string]*Entry)
		r.sessions[sessionID] = pages
	}
	if len(pages) >= r.cfg.MaxPages {
		_ = held.Close(ctx)
		return nil, r.capacityErrorLocked(sessionID)
	}
	now := time.Now()
	id := uuid.NewString()
	e := &Entry{
		ID: id, SessionID: sessionID, Held: held,
		TargetURL: held.TargetURL, OpenedAt: now, LastUsed: now,
	}
	pages[id] = e
	return e, nil
}

// RequireRunning returns the live entry and bumps LastUsed.
func (r *Registry) RequireRunning(sessionID, pageID string) (*Entry, error) {
	if r == nil {
		return nil, ErrPageNotFound
	}
	sessionID = strings.TrimSpace(sessionID)
	pageID = strings.TrimSpace(pageID)
	r.mu.Lock()
	defer r.mu.Unlock()
	pages := r.sessions[sessionID]
	if pages == nil {
		return nil, ErrPageNotFound
	}
	e := pages[pageID]
	if e == nil {
		return nil, ErrPageNotFound
	}
	if e.Held == nil || e.Held.Page == nil {
		delete(pages, pageID)
		return nil, ErrPageNotRunning
	}
	e.LastUsed = time.Now()
	return e, nil
}

// Close tears down one page.
func (r *Registry) ClosePage(ctx context.Context, sessionID, pageID string) error {
	if r == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	pageID = strings.TrimSpace(pageID)
	r.mu.Lock()
	pages := r.sessions[sessionID]
	var e *Entry
	if pages != nil {
		e = pages[pageID]
		delete(pages, pageID)
		if len(pages) == 0 {
			delete(r.sessions, sessionID)
		}
	}
	r.mu.Unlock()
	if e == nil {
		return ErrPageNotFound
	}
	err := e.Held.Close(ctx)
	r.fireClose(sessionID, pageID)
	return err
}

// DisposeSession closes every held page and reports any browser close failure.
func (r *Registry) DisposeSession(ctx context.Context, sessionID string) error {
	if r == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	r.mu.Lock()
	pages := r.sessions[sessionID]
	delete(r.sessions, sessionID)
	r.mu.Unlock()
	var errs []error
	for id, e := range pages {
		if err := e.Held.Close(ctx); err != nil {
			errs = append(errs, err)
		}
		r.fireClose(sessionID, id)
	}
	return errors.Join(errs...)
}

func (r *Registry) fireClose(sessionID, pageID string) {
	r.mu.Lock()
	fn := r.onClose
	r.mu.Unlock()
	if fn != nil {
		fn(sessionID, pageID)
	}
}

// List returns live page IDs for a session.
func (r *Registry) List(sessionID string) []string {
	if r == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	r.mu.Lock()
	defer r.mu.Unlock()
	pages := r.sessions[sessionID]
	if len(pages) == 0 {
		return nil
	}
	out := make([]string, 0, len(pages))
	for id := range pages {
		out = append(out, id)
	}
	return out
}

// CountLive returns the number of live pages for sessionID.
func (r *Registry) CountLive(sessionID string) int {
	return len(r.List(sessionID))
}

// TargetURL returns the target a live page is showing, or "" when the id names
// none. It leaves LastUsed alone, so reading a fact cannot delay a reap.
func (r *Registry) TargetURL(sessionID, pageID string) string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.sessions[strings.TrimSpace(sessionID)][strings.TrimSpace(pageID)]
	if e == nil {
		return ""
	}
	return e.TargetURL
}

// FindByTarget returns the oldest live page already showing target.
func (r *Registry) FindByTarget(sessionID string, target browser.PageTarget) (string, bool) {
	if r == nil {
		return "", false
	}
	want := strings.TrimSpace(target.URL)
	if want == "" {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var (
		foundID string
		oldest  time.Time
	)
	for id, e := range r.sessions[strings.TrimSpace(sessionID)] {
		if e == nil || e.TargetURL != want || e.Held == nil || e.Held.RootDir != target.RootDir {
			continue
		}
		if foundID == "" || e.OpenedAt.Before(oldest) {
			foundID, oldest = id, e.OpenedAt
		}
	}
	return foundID, foundID != ""
}

func (r *Registry) reapLoop() {
	defer close(r.done)
	ticker := time.NewTicker(reapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			r.reapIdle()
		}
	}
}

func (r *Registry) reapIdle() {
	cutoff := time.Now().Add(-r.cfg.IdleTimeout)
	type victim struct {
		sessionID, pageID string
		held              *browser.HeldPage
	}
	var kill []victim
	r.mu.Lock()
	for sid, pages := range r.sessions {
		for id, e := range pages {
			if e.LastUsed.Before(cutoff) {
				kill = append(kill, victim{sid, id, e.Held})
				delete(pages, id)
			}
		}
		if len(pages) == 0 {
			delete(r.sessions, sid)
		}
	}
	r.mu.Unlock()
	for _, v := range kill {
		_ = v.held.Close(context.Background())
		r.fireClose(v.sessionID, v.pageID)
	}
}

// CapacityError freezes the refusing session's admission state.
type CapacityError struct {
	Limit int
	IDs   []string
}

func (e *CapacityError) Error() string { return ErrPageCapReached.Error() }
func (e *CapacityError) Unwrap() error { return ErrPageCapReached }

func (r *Registry) capacityErrorLocked(sessionID string) *CapacityError {
	ids := make([]string, 0, len(r.sessions[sessionID]))
	for id := range r.sessions[sessionID] {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return &CapacityError{Limit: r.cfg.MaxPages, IDs: ids}
}

// MaxPages returns the configured per-session cap.
func (r *Registry) MaxPages() int {
	if r == nil {
		return DefaultMaxPages
	}
	return r.cfg.MaxPages
}
