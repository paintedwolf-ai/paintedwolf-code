package turnload

import "strings"

// FullCatalog reports whether selection has yielded to complete visibility.
func (l *Ledger) FullCatalog(sessionID string) bool {
	if l == nil {
		return false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	s := l.sessions[strings.TrimSpace(sessionID)]
	return s != nil && s.fullCatalog
}

// ExposeCatalog expands the current permitted set without inventing requests.
func (l *Ledger) ExposeCatalog(sessionID string, failure RankingFailure) {
	if l == nil || strings.TrimSpace(sessionID) == "" || !failure.RequiresFullCatalog() {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.session(sessionID).exposeCatalog()
}

func (s *sessionLoads) exposeCatalog() {
	s.fullCatalog = true
	s.omitted = map[string]bool{}
	for name := range s.loadable {
		if _, exists := s.standing[name]; !exists {
			s.standing[name] = Load{Tool: name, Source: SourceFallback, Turn: s.openingMessageID}
		}
	}
}

// ReconcileCatalog applies current permission filtering before a continuation.
func (l *Ledger) ReconcileCatalog(sessionID string, loadable []string, failure RankingFailure) bool {
	if l == nil || strings.TrimSpace(sessionID) == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.session(sessionID)
	s.loadable = make(map[string]bool, len(loadable))
	for _, name := range loadable {
		if name = strings.TrimSpace(name); name != "" {
			s.loadable[name] = true
		}
	}
	changed := false
	for name := range s.standing {
		if !s.loadable[name] {
			changed = true
			delete(s.standing, name)
		}
	}
	wasFull, count := s.fullCatalog, len(s.standing)
	if s.fullCatalog || failure.RequiresFullCatalog() {
		s.exposeCatalog()
	}
	return changed || wasFull != s.fullCatalog || count != len(s.standing)
}
