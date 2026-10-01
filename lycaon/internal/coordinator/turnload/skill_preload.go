package turnload

import "strings"

// SkillPreload is the rendered procedure selected for one turn.
type SkillPreload struct {
	Name  string  `json:"name"`
	Score float64 `json:"score"`
	Body  string  `json:"body"`
	// Tool names the loadable tool whose first call selected the skill;
	// empty when the turn's opening decision selected it.
	Tool string `json:"tool,omitempty"`
}

func clonePreload(p *SkillPreload) *SkillPreload {
	if p == nil {
		return nil
	}
	copy := *p
	return &copy
}

func (l *Ledger) Preload(sessionID string) *SkillPreload {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	s := l.sessions[strings.TrimSpace(sessionID)]
	if s == nil {
		return nil
	}
	return clonePreload(s.preload)
}

func (l *Ledger) SetPreload(sessionID string, preload *SkillPreload) {
	if l == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.session(sessionID).preload = clonePreload(preload)
}
