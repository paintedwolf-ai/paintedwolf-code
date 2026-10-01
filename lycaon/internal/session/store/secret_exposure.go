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

func (s *Memory) SessionSecretExposure(ctx context.Context, sessionID string) (bool, error) {
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

func (s *SQL) SessionSecretExposure(ctx context.Context, sessionID string) (bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false, nil
	}
	s.secretExposureMu.Lock()
	defer s.secretExposureMu.Unlock()
	if cached, ok := s.secretExposureRecords.Load(sessionID); ok && cached.warm {
		return cached.n > 0, nil
	}
	n, err := s.countSecretExposureRecords(ctx, sessionID)
	if err != nil {
		return false, err
	}
	s.secretExposureRecords.Store(sessionID, secretExposureCount{n: n, warm: true})
	return n > 0, nil
}

func (s *SQL) countSecretExposureRecords(ctx context.Context, sessionID string) (int, error) {
	ev, err := s.LoadLedger(ctx, sessionID)
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

func (s *Memory) bumpSecretExposureLocked(sessionID string, rec evidence.Record) {
	if !evidence.RecordMarksSecretExposure(rec) {
		return
	}
	if s.secretExposureRecords == nil {
		s.secretExposureRecords = make(map[string]int)
	}
	s.secretExposureRecords[sessionID]++
}

func (s *SQL) bumpSecretExposure(sessionID string, rec evidence.Record) {
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

func (s *SQL) invalidateSecretExposure(sessionID string) {
	s.secretExposureMu.Lock()
	defer s.secretExposureMu.Unlock()
	s.secretExposureRecords.Delete(sessionID)
}

// SeedSecretExposure records inherited secret exposure.
func (s *Memory) SeedSecretExposure(ctx context.Context, sessionID string) error {
	return s.UpsertEvidenceRecord(ctx, sessionID, inheritSecretExposureRecord())
}

func (s *SQL) SeedSecretExposure(ctx context.Context, sessionID string) error {
	return s.UpsertEvidenceRecord(ctx, sessionID, inheritSecretExposureRecord())
}

func inheritSecretExposureRecord() evidence.Record {
	return evidence.Record{
		Handle:   evidence.InheritSecretExposureHandle,
		Kind:     "file",
		Fidelity: evidence.FidelityStructured,
	}
}

// MarkSecretExposureOnRead records a successful credential-path read.
func (s *SQL) MarkSecretExposureOnRead(ctx context.Context, sessionID, relPath string) error {
	rec, ok := secretExposureReadRecord(relPath)
	if !ok {
		return nil
	}
	return s.UpsertEvidenceRecord(ctx, sessionID, rec)
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
