package store

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
)

func (ev *memoryEvidence) SessionUntrustedContent(sessionID string) bool {
	s := ev.core
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.untrustedRecords[sessionID] > 0
}

func (ev *memoryEvidence) SessionUntrustedContentResult(ctx context.Context, sessionID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return ev.SessionUntrustedContent(sessionID), nil
}

// untrustedCount tracks the cached count and initialization state.
type untrustedCount struct {
	n    int
	warm bool
}

// SessionUntrustedContent reads the cache with a background context.
func (ev *sqlEvidence) SessionUntrustedContent(sessionID string) bool {
	value, _ := sessionUntrustedContentResultSQL(context.Background(), ev.core, sessionID)
	return value
}

func (ev *sqlEvidence) SessionUntrustedContentResult(ctx context.Context, sessionID string) (bool, error) {
	return sessionUntrustedContentResultSQL(ctx, ev.core, sessionID)
}

func sessionUntrustedContentResultSQL(ctx context.Context, s *sqlCore, sessionID string) (bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false, nil
	}
	s.untrustedMu.Lock()
	defer s.untrustedMu.Unlock()
	if cached, ok := s.untrustedRecords.Load(sessionID); ok && cached.warm {
		return cached.n > 0, nil
	}
	n, err := countUntrustedRecords(ctx, s, sessionID)
	if err != nil {
		return false, err
	}
	s.untrustedRecords.Store(sessionID, untrustedCount{n: n, warm: true})
	return n > 0, nil
}

func countUntrustedRecords(ctx context.Context, s *sqlCore, sessionID string) (int, error) {
	n, err := s.queries.HasUntrustedEvidence(ctx, sessionID)
	if err != nil || n == 0 {
		return 0, err
	}
	return 1, nil
}

func bumpUntrustedLocked(s *memoryCore, sessionID string, rec evidence.Record) {
	if !evidence.RecordMarksUntrustedContent(rec) {
		return
	}
	if s.untrustedRecords == nil {
		s.untrustedRecords = make(map[string]int)
	}
	s.untrustedRecords[sessionID]++
}

func bumpUntrusted(s *sqlCore, sessionID string, rec evidence.Record) {
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

func invalidateUntrusted(s *sqlCore, sessionID string) {
	s.untrustedMu.Lock()
	defer s.untrustedMu.Unlock()
	s.untrustedRecords.Delete(sessionID)
}

// SeedUntrustedContent records inherited untrusted content.
func (ev *memoryEvidence) SeedUntrustedContent(ctx context.Context, sessionID string) error {
	return upsertEvidenceRecordMemory(ctx, ev.core, sessionID, inheritUntrustedRecord())
}

func (ev *sqlEvidence) SeedUntrustedContent(ctx context.Context, sessionID string) error {
	return upsertEvidenceRecordSQL(ctx, ev.core, sessionID, inheritUntrustedRecord())
}

func inheritUntrustedRecord() evidence.Record {
	return evidence.Record{
		Handle:   evidence.InheritUntrustedHandle,
		Kind:     "web",
		Shape:    evidence.ShapeURL,
		Fidelity: evidence.FidelityStructured,
	}
}

func (ev *memoryEvidence) SessionVisitedHosts(sessionID string) map[string]struct{} {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	ledger, err := loadLedgerMemory(context.Background(), ev.core, sessionID)
	if err != nil {
		return nil
	}
	return evidence.VisitedHostsFromLedger(ledger)
}

func (ev *sqlEvidence) SessionVisitedHosts(sessionID string) map[string]struct{} {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	ledger, err := loadLedger(context.Background(), ev.core, ev.core.queries, sessionID)
	if err != nil {
		return nil
	}
	return evidence.VisitedHostsFromLedger(ledger)
}

// RecordHostVisit adds a mediated dial to the chat's visited-host set. The record
// carries no content tool, so it does not mark the session untrusted.
func (ev *memoryEvidence) RecordHostVisit(ctx context.Context, sessionID, host string) error {
	rec, ok := evidence.DialedHostRecord(host)
	if !ok || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	return upsertEvidenceRecordMemory(ctx, ev.core, sessionID, rec)
}

// RecordHostVisit adds a mediated dial to the chat's visited-host set.
func (ev *sqlEvidence) RecordHostVisit(ctx context.Context, sessionID, host string) error {
	rec, ok := evidence.DialedHostRecord(host)
	if !ok || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	return upsertEvidenceRecordSQL(ctx, ev.core, sessionID, rec)
}
