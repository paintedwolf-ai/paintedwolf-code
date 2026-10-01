package renderhandle

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/browser"
)

var (
	// ErrHandleNotFound indicates the requested view handle does not exist.
	ErrHandleNotFound = errors.New("render handle not found")
	// ErrHandleConflict indicates the handle changed after the caller read it.
	ErrHandleConflict = errors.New("render handle changed concurrently")
	// ErrPatchNotFound indicates the patch target string was not found in the markup.
	ErrPatchNotFound = errors.New("patch target string not found")
	// ErrPatchAmbiguous indicates the patch target string matched multiple locations and replace_all was false.
	ErrPatchAmbiguous = errors.New("patch target string matches multiple locations")
	// ErrPatchEmptyTarget indicates old_string was empty.
	ErrPatchEmptyTarget = errors.New("patch target string cannot be empty")
)

const (
	// MaxHandlesPerSession bounds live handles in one session.
	MaxHandlesPerSession = 16
	// MaxSessionBytes bounds retained raster bytes in one session, matching the
	// hot visual budget of a session tree.
	MaxSessionBytes = 32 << 20
)

// ViewportConfig stores the layout and raster sizing preferences for a handle.
type ViewportConfig struct {
	Preset string  `json:"preset,omitempty"`
	Width  int     `json:"width,omitempty"`
	Height int     `json:"height,omitempty"`
	Fit    string  `json:"fit,omitempty"`
	Scale  float64 `json:"scale,omitempty"`
}

// RenderHandle represents a stateful, iterative rendered view in a session.
type RenderHandle struct {
	ID        string               `json:"id"`
	SessionID string               `json:"session_id"`
	Markup    string               `json:"markup"`
	Mime      string               `json:"mime"`
	Theme     string               `json:"theme,omitempty"`
	Viewport  ViewportConfig       `json:"viewport,omitempty"`
	Fonts     []string             `json:"fonts,omitempty"`
	Caption   string               `json:"caption,omitempty"`
	Bytes     []byte               `json:"-"`
	Canvas    browser.RenderCanvas `json:"canvas"`
	Revision  int                  `json:"revision"`
	CreatedAt time.Time            `json:"created_at"`
	UpdatedAt time.Time            `json:"updated_at"`
}

// Clone creates a deep copy of the handle.
func (h *RenderHandle) Clone() *RenderHandle {
	if h == nil {
		return nil
	}
	cp := *h
	if len(h.Fonts) > 0 {
		cp.Fonts = append([]string(nil), h.Fonts...)
	}
	if len(h.Bytes) > 0 {
		cp.Bytes = append([]byte(nil), h.Bytes...)
	}
	return &cp
}

// Store keeps session-scoped render handles in memory. A handle changes only
// through Put, once per successful render, so a failed render leaves the last
// committed revision in place.
type Store interface {
	Get(sessionID, handleID string) (*RenderHandle, bool)
	// Put commits a rendered revision. base is the revision the caller read;
	// zero replaces whatever is stored. A stale base returns ErrHandleConflict.
	Put(sessionID string, h *RenderHandle, base int) (*RenderHandle, error)
	// Patch returns the stored handle with one replacement applied, without
	// storing it; its Revision is the base to pass to Put.
	Patch(sessionID, handleID, oldString, newString string, replaceAll bool) (*RenderHandle, error)
	List(sessionID string) []*RenderHandle
	// Release drops every handle of an ended session.
	Release(sessionID string)
}

type entry struct {
	handle *RenderHandle
	used   uint64
}

type memoryStore struct {
	mu       sync.Mutex
	handles  map[string]map[string]*entry
	clock    uint64
	maxCount int
	maxBytes int
}

// NewStore creates the in-memory store with the production session bounds.
func NewStore() Store {
	return newStore(MaxHandlesPerSession, MaxSessionBytes)
}

func newStore(maxCount, maxBytes int) *memoryStore {
	return &memoryStore{
		handles:  make(map[string]map[string]*entry),
		maxCount: maxCount,
		maxBytes: maxBytes,
	}
}

func keys(sessionID, handleID string) (string, string, bool) {
	sessionID = strings.TrimSpace(sessionID)
	handleID = strings.TrimSpace(handleID)
	return sessionID, handleID, sessionID != "" && handleID != ""
}

func (s *memoryStore) touch(e *entry) {
	s.clock++
	e.used = s.clock
}

func (s *memoryStore) Get(sessionID, handleID string) (*RenderHandle, bool) {
	sessionID, handleID, ok := keys(sessionID, handleID)
	if !ok {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.handles[sessionID][handleID]
	if !ok {
		return nil, false
	}
	s.touch(e)
	return e.handle.Clone(), true
}

func (s *memoryStore) Put(sessionID string, h *RenderHandle, base int) (*RenderHandle, error) {
	if h == nil {
		return nil, ErrHandleNotFound
	}
	sessionID, handleID, ok := keys(sessionID, h.ID)
	if !ok {
		return nil, ErrHandleNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	bySession := s.handles[sessionID]
	if bySession == nil {
		bySession = make(map[string]*entry)
		s.handles[sessionID] = bySession
	}
	now := time.Now().UTC()
	stored := h.Clone()
	stored.SessionID = sessionID
	stored.ID = handleID
	stored.UpdatedAt = now

	existing, exists := bySession[handleID]
	switch {
	case exists && base > 0 && existing.handle.Revision != base:
		return nil, fmt.Errorf("%w: revision %d, read %d", ErrHandleConflict, existing.handle.Revision, base)
	case !exists && base > 0:
		return nil, ErrHandleConflict
	case exists:
		stored.Revision = existing.handle.Revision + 1
		stored.CreatedAt = existing.handle.CreatedAt
	default:
		stored.Revision = 1
		stored.CreatedAt = now
	}
	e := &entry{handle: stored}
	s.touch(e)
	bySession[handleID] = e
	s.evict(bySession, handleID)
	return stored.Clone(), nil
}

// evict drops least recently used handles until the session fits its bounds.
// The handle just written is kept even when it alone exceeds the byte bound.
func (s *memoryStore) evict(bySession map[string]*entry, keep string) {
	total := 0
	for _, e := range bySession {
		total += len(e.handle.Bytes)
	}
	for len(bySession) > s.maxCount || (total > s.maxBytes && len(bySession) > 1) {
		victim := ""
		var oldest uint64
		for id, e := range bySession {
			if id == keep {
				continue
			}
			if victim == "" || e.used < oldest {
				victim, oldest = id, e.used
			}
		}
		if victim == "" {
			return
		}
		total -= len(bySession[victim].handle.Bytes)
		delete(bySession, victim)
	}
}

func (s *memoryStore) Patch(sessionID, handleID, oldString, newString string, replaceAll bool) (*RenderHandle, error) {
	sessionID, handleID, ok := keys(sessionID, handleID)
	if !ok {
		return nil, ErrHandleNotFound
	}
	if oldString == "" {
		return nil, ErrPatchEmptyTarget
	}
	s.mu.Lock()
	e, ok := s.handles[sessionID][handleID]
	var current *RenderHandle
	if ok {
		s.touch(e)
		current = e.handle.Clone()
	}
	s.mu.Unlock()
	if !ok {
		return nil, ErrHandleNotFound
	}

	count := strings.Count(current.Markup, oldString)
	if count == 0 {
		return nil, fmt.Errorf("%w: %q", ErrPatchNotFound, oldString)
	}
	if count > 1 && !replaceAll {
		return nil, fmt.Errorf("%w: %q matched %d times", ErrPatchAmbiguous, oldString, count)
	}
	if replaceAll {
		current.Markup = strings.ReplaceAll(current.Markup, oldString, newString)
	} else {
		current.Markup = strings.Replace(current.Markup, oldString, newString, 1)
	}
	return current, nil
}

func (s *memoryStore) List(sessionID string) []*RenderHandle {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	bySession := s.handles[sessionID]
	out := make([]*RenderHandle, 0, len(bySession))
	for _, e := range bySession {
		out = append(out, e.handle.Clone())
	}
	return out
}

func (s *memoryStore) Release(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	s.mu.Lock()
	delete(s.handles, sessionID)
	s.mu.Unlock()
}
