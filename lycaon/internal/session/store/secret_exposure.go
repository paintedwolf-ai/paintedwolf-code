package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/protectedpath"
)

func (ev *memoryEvidence) SessionSecretExposure(ctx context.Context, sessionID string) (bool, error) {
	s := ev.core
	if err := ctx.Err(); err != nil {
		return false, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.secretExposureRecords[sessionID] > 0, nil
}

type secretExposureCount struct {
	n    int
	warm bool
}

func (ev *sqlEvidence) SessionSecretExposure(ctx context.Context, sessionID string) (bool, error) {
	s := ev.core
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false, nil
	}
	s.secretExposureMu.Lock()
	defer s.secretExposureMu.Unlock()
	if cached, ok := s.secretExposureRecords.Load(sessionID); ok && cached.warm {
		return cached.n > 0, nil
	}
	n, err := countSecretExposureRecords(ctx, s, sessionID)
	if err != nil {
		return false, err
	}
	s.secretExposureRecords.Store(sessionID, secretExposureCount{n: n, warm: true})
	return n > 0, nil
}

func countSecretExposureRecords(ctx context.Context, s *sqlCore, sessionID string) (int, error) {
	ev, err := loadLedger(ctx, s, s.queries, sessionID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, rec := range ev.Handles {
		if evidence.RecordMarksSecretExposure(rec) {
			n++
		}
	}
	return n, nil
}

func bumpSecretExposureLocked(s *memoryCore, sessionID string, rec evidence.Record) {
	if !evidence.RecordMarksSecretExposure(rec) {
		return
	}
	if s.secretExposureRecords == nil {
		s.secretExposureRecords = make(map[string]int)
	}
	s.secretExposureRecords[sessionID]++
}

func bumpSecretExposure(s *sqlCore, sessionID string, rec evidence.Record) {
	if !evidence.RecordMarksSecretExposure(rec) {
		return
	}
	s.secretExposureMu.Lock()
	defer s.secretExposureMu.Unlock()
	if cached, ok := s.secretExposureRecords.Load(sessionID); ok && cached.warm {
		cached.n++
		s.secretExposureRecords.Store(sessionID, cached)
		return
	}
	s.secretExposureRecords.Delete(sessionID)
}

func invalidateSecretExposure(s *sqlCore, sessionID string) {
	s.secretExposureMu.Lock()
	defer s.secretExposureMu.Unlock()
	s.secretExposureRecords.Delete(sessionID)
}

// SeedSecretExposure records inherited secret exposure.
func (ev *memoryEvidence) SeedSecretExposure(ctx context.Context, sessionID string) error {
	return upsertEvidenceRecordMemory(ctx, ev.core, sessionID, inheritSecretExposureRecord())
}

func (ev *sqlEvidence) SeedSecretExposure(ctx context.Context, sessionID string) error {
	return upsertEvidenceRecordSQL(ctx, ev.core, sessionID, inheritSecretExposureRecord())
}

func inheritSecretExposureRecord() evidence.Record {
	return evidence.Record{
		Handle:   evidence.InheritSecretExposureHandle,
		Kind:     "file",
		Fidelity: evidence.FidelityStructured,
	}
}

// MarkSecretExposureOnRead records a successful credential-path read.
func (ev *sqlEvidence) MarkSecretExposureOnRead(ctx context.Context, sessionID, relPath string) error {
	rec, ok := secretExposureReadRecord(relPath)
	if !ok {
		return nil
	}
	return upsertEvidenceRecordSQL(ctx, ev.core, sessionID, rec)
}

func secretExposureReadRecord(relPath string) (evidence.Record, bool) {
	rel := filepath.ToSlash(strings.TrimSpace(relPath))
	if rel == "" || !protectedpath.IsCredentialFile(rel) {
		return evidence.Record{}, false
	}
	sum := sha256.Sum256([]byte(rel))
	return evidence.Record{
		Handle:     evidence.SecretExposureHandlePrefix + hex.EncodeToString(sum[:8]),
		Kind:       "file",
		Fidelity:   evidence.FidelityStructured,
		Path:       rel,
		SourceTool: "read",
	}, true
}

func evidencePathArg(args map[string]any) string {
	if args == nil {
		return ""
	}
	raw, _ := args["path"].(string)
	return filepath.ToSlash(strings.TrimSpace(raw))
}
