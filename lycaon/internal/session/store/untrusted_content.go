package store

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
)

func (s *Memory) SessionUntrustedContent(sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.untrustedRecords[sessionID] > 0
}

func (s *Memory) SessionUntrustedContentResult(ctx context.Context, sessionID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return s.SessionUntrustedContent(sessionID), nil
}

// untrustedCount tracks the cached count and initialization state.
type untrustedCount struct {
	n    int
	warm bool
}

// SessionUntrustedContent reads the cache with a background context.
func (s *SQL) SessionUntrustedContent(sessionID string) bool {
	value, _ := s.SessionUntrustedContentResult(context.Background(), sessionID)
	return value
}

func (s *SQL) SessionUntrustedContentResult(ctx context.Context, sessionID string) (bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false, nil
	}
	s.untrustedMu.Lock()
	defer s.untrustedMu.Unlock()
	if cached, ok := s.untrustedRecords.Load(sessionID); ok && cached.warm {
		return cached.n > 0, nil
	}
	n, err := s.countUntrustedRecords(ctx, sessionID)
	if err != nil {
		return false, err
	}
	s.untrustedRecords.Store(sessionID, untrustedCount{n: n, warm: true})
	return n > 0, nil
}

func (s *SQL) countUntrustedRecords(ctx context.Context, sessionID string) (int, error) {
	n, err := s.queries.HasUntrustedEvidence(ctx, sessionID)
	if err != nil || n == 0 {
		return 0, err
	}
	return 1, nil
}

func (s *Memory) bumpUntrustedLocked(sessionID string, rec evidence.Record) {
	if !evidence.RecordMarksUntrustedContent(rec) {
		return
	}
	if s.untrustedRecords == nil {
		s.untrustedRecords = make(map[string]int)
	}
	s.untrustedRecords[sessionID]++
}

func (s *SQL) bumpUntrusted(sessionID string, rec evidence.Record) {
	if !evidence.RecordMarksUntrustedContent(rec) {
		return
	}
	s.untrustedMu.Lock()
	defer s.untrustedMu.Unlock()
	if cached, ok := s.untrustedRecords.Load(sessionID); ok && cached.warm {
		cached.n++
		s.untrustedRecords.Store(sessionID, cached)
		return
	}
	// A cold cache rebuilds from durable rows.
	s.untrustedRecords.Delete(sessionID)
}

func (s *SQL) invalidateUntrusted(sessionID string) {
	s.untrustedMu.Lock()
	defer s.untrustedMu.Unlock()
	s.untrustedRecords.Delete(sessionID)
}

// SeedUntrustedContent records inherited untrusted content.
func (s *Memory) SeedUntrustedContent(ctx context.Context, sessionID string) error {
	return s.UpsertEvidenceRecord(ctx, sessionID, inheritUntrustedRecord())
}

func (s *SQL) SeedUntrustedContent(ctx context.Context, sessionID string) error {
	return s.UpsertEvidenceRecord(ctx, sessionID, inheritUntrustedRecord())
}

func inheritUntrustedRecord() evidence.Record {
	return evidence.Record{
		Handle:   evidence.InheritUntrustedHandle,
		Kind:     "web",
		Shape:    evidence.ShapeURL,
		Fidelity: evidence.FidelityStructured,
	}
}

func (s *Memory) SessionVisitedHosts(sessionID string) map[string]struct{} {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	ev, err := s.LoadLedger(context.Background(), sessionID)
	if err != nil {
		return nil
	}
	return evidence.VisitedHostsFromLedger(ev)
}

func (s *SQL) SessionVisitedHosts(sessionID string) map[string]struct{} {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	ev, err := s.LoadLedger(context.Background(), sessionID)
	if err != nil {
		return nil
	}
	return evidence.VisitedHostsFromLedger(ev)
}

// RecordHostVisit adds a mediated dial to the chat's visited-host set. The record
// carries no content tool, so it does not mark the session untrusted.
func (s *Memory) RecordHostVisit(ctx context.Context, sessionID, host string) error {
	rec, ok := evidence.DialedHostRecord(host)
	if !ok || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	return s.UpsertEvidenceRecord(ctx, sessionID, rec)
}

// RecordHostVisit adds a mediated dial to the chat's visited-host set.
func (s *SQL) RecordHostVisit(ctx context.Context, sessionID, host string) error {
	rec, ok := evidence.DialedHostRecord(host)
	if !ok || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	return s.UpsertEvidenceRecord(ctx, sessionID, rec)
}
