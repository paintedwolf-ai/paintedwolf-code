package settings

import (
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/scopedstore"
)

// askQuiets stores chat-keyed quiet records. Eviction re-arms the gate (safe).
// Retention: Bounded at scopedstore.DefaultEntries per chat key domain.
type askQuiets struct {
	mu     sync.Mutex
	byChat scopedstore.Map[map[string]hitl.AskQuiet]
	byID   map[string]string
}

func newAskQuiets() *askQuiets { return &askQuiets{} }

// Put preserves a live quiet with the same id.
func (s *askQuiets) put(q hitl.AskQuiet, ttlSeconds int) (hitl.AskQuiet, bool) {
	if hitl.ValidateElevatedEffects(q.ElevatedEffects) != nil {
		return hitl.AskQuiet{}, false
	}
	chat := strings.TrimSpace(q.ChatSessionID)
	key := strings.TrimSpace(q.Key)
	if chat == "" || key == "" {
		return hitl.AskQuiet{}, false
	}
	if q.ID == "" {
		q.ID = hitl.QuietRecordID(chat, key, ttlSeconds)
	}
	q.ElevatedEffects = slices.Clone(q.ElevatedEffects)
	q.ChatSessionID = chat
	q.Key = key
	q.Label = strings.TrimSpace(q.Label)
	q.OwnerOperationID = strings.TrimSpace(q.OwnerOperationID)
	now := time.Now().UTC()
	if q.CreatedAt.IsZero() {
		q.CreatedAt = now
	}
	if ttlSeconds > 0 {
		exp := now.Add(time.Duration(ttlSeconds) * time.Second)
		q.ExpiresAt = &exp
	} else {
		q.ExpiresAt = nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	set, ok := s.byChat.Load(chat)
	if !ok || set == nil {
		set = map[string]hitl.AskQuiet{}
	}
	if prev, existed := set[q.ID]; existed && (prev.ExpiresAt == nil || prev.ExpiresAt.After(now)) {
		return prev, false
	}
	set[q.ID] = q
	s.byChat.Store(chat, set)
	if s.byID == nil {
		s.byID = map[string]string{}
	}
	s.byID[q.ID] = chat
	if len(s.byID) > 512 {
		for id, candidateChat := range s.byID {
			if _, active := s.byChat.Load(candidateChat); !active {
				delete(s.byID, id)
			}
		}
	}
	return q, true
}

func (s *askQuiets) live(chat, key string) (hitl.AskQuiet, bool) {
	chat = strings.TrimSpace(chat)
	key = strings.TrimSpace(key)
	if chat == "" || key == "" {
		return hitl.AskQuiet{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	set, ok := s.byChat.Load(chat)
	if !ok {
		return hitl.AskQuiet{}, false
	}
	now := time.Now()
	for _, q := range set {
		if q.Key != key {
			continue
		}
		if q.ExpiresAt != nil && !q.ExpiresAt.After(now) {
			continue
		}
		return q, true
	}
	return hitl.AskQuiet{}, false
}

func (s *askQuiets) noteSuppressed(chat, key string) {
	chat = strings.TrimSpace(chat)
	key = strings.TrimSpace(key)
	if chat == "" || key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	set, ok := s.byChat.Load(chat)
	if !ok {
		return
	}
	now := time.Now()
	for id, q := range set {
		if q.Key != key {
			continue
		}
		if q.ExpiresAt != nil && !q.ExpiresAt.After(now) {
			continue
		}
		q.Suppressed++
		set[id] = q
		s.byChat.Store(chat, set)
		return
	}
}

func (s *askQuiets) list(chat string) []hitl.AskQuiet {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var out []hitl.AskQuiet
	appendLive := func(set map[string]hitl.AskQuiet) {
		for _, q := range set {
			if q.ExpiresAt != nil && !q.ExpiresAt.After(now) {
				continue
			}
			q.ElevatedEffects = slices.Clone(q.ElevatedEffects)
			out = append(out, q)
		}
	}
	if chat != "" {
		if set, ok := s.byChat.Load(chat); ok {
			appendLive(set)
		}
		return out
	}
	for _, set := range s.byChat.Snapshot() {
		appendLive(set)
	}
	return out
}

func (s *askQuiets) revoke(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" || !strings.HasPrefix(id, "quiet_") {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	chat, ok := s.byID[id]
	if !ok {
		return false
	}
	set, ok := s.byChat.Load(chat)
	if ok {
		delete(set, id)
		s.byChat.Store(chat, set)
	}
	delete(s.byID, id)
	return true
}

func (s *askQuiets) revokeInstalledBy(id, operationID string) bool {
	id = strings.TrimSpace(id)
	operationID = strings.TrimSpace(operationID)
	if id == "" || operationID == "" || !strings.HasPrefix(id, "quiet_") {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	chat, ok := s.byID[id]
	if !ok {
		return false
	}
	set, ok := s.byChat.Load(chat)
	if !ok || set[id].OwnerOperationID != operationID {
		return false
	}
	delete(set, id)
	s.byChat.Store(chat, set)
	delete(s.byID, id)
	return true
}

func (s *askQuiets) forget(chat string) {
	chat = strings.TrimSpace(chat)
	if chat == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if set, ok := s.byChat.Load(chat); ok {
		for id := range set {
			delete(s.byID, id)
		}
	}
	s.byChat.Delete(chat)
}
