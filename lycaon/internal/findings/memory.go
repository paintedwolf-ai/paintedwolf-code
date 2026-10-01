package findings

import (
	"context"
	"strings"
	"sync"
	"time"
)

// MemoryStore is an in-memory Store for tests.
type MemoryStore struct {
	mu         sync.Mutex
	bySess     map[string][]Finding
	nextID     int64
	deliveries map[string]Delivery
	responses  map[string]string
}

var _ Store = (*MemoryStore)(nil)

// NewMemoryStore returns an empty in-memory findings store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{bySess: make(map[string][]Finding), deliveries: make(map[string]Delivery), responses: make(map[string]string)}
}

func (s *MemoryStore) Append(_ context.Context, sessionID, agent, summary, ref, body string) (bool, error) {
	if s == nil {
		return false, nil
	}
	key := normalizeKey(sessionID)
	agent, summary, ref = normalizeFindingFields(agent, summary, ref)
	if key == "" || summary == "" {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.bySess[key] {
		if findingMatches(f, agent, summary, ref) && f.Body == body {
			return false, nil
		}
	}
	s.nextID++
	s.bySess[key] = append(s.bySess[key], Finding{
		ID:      s.nextID,
		Agent:   agent,
		Summary: summary,
		Body:    body,
		Ref:     ref,
		TS:      time.Now().UTC(),
	})
	return true, nil
}

func (s *MemoryStore) Recent(_ context.Context, sessionID, excludeAgent string, afterID int64, limit int, since time.Time) ([]Finding, int64, error) {
	if s == nil {
		return nil, afterID, nil
	}
	key := normalizeKey(sessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	all := filterSince(s.bySess[key], since)
	excludeAgent = strings.TrimSpace(excludeAgent)
	var out []Finding
	for _, finding := range all {
		if finding.ID <= afterID {
			continue
		}
		if limit > 0 && len(out) >= limit {
			break
		}
		afterID = max(afterID, finding.ID)
		if excludeAgent != "" && finding.Agent == excludeAgent {
			continue
		}
		out = append(out, finding)
	}
	return out, afterID, nil
}

func (s *MemoryStore) List(_ context.Context, sessionID string, max int) ([]Finding, error) {
	if s == nil {
		return nil, nil
	}
	key := normalizeKey(sessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	all := s.bySess[key]
	if max > 0 && len(all) > max {
		all = all[len(all)-max:]
	}
	out := make([]Finding, len(all))
	copy(out, all)
	return out, nil
}

func (s *MemoryStore) Get(_ context.Context, sessionID string, id int64) (Finding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.bySess[normalizeKey(sessionID)] {
		if f.ID == id {
			return f, nil
		}
	}
	return Finding{}, ErrNotFound
}

func (s *MemoryStore) Delivery(_ context.Context, job string) (Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.deliveries[job]
	d.Notes = append([]Finding(nil), d.Notes...)
	return d, nil
}
func (s *MemoryStore) CommitDelivery(_ context.Context, job, response string, d Delivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if prior, exists := s.deliveries[job]; exists && (d.Cursor <= prior.Cursor || s.responses[job] == response) {
		return nil
	}
	s.responses[job] = response
	d.Notes = append([]Finding(nil), d.Notes...)
	s.deliveries[job] = d
	return nil
}
