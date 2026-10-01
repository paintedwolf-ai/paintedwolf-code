package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/visual"
)

func (s *Memory) ensureEvidenceSession(sessionID string) map[string]memoryEvidenceRow {
	if s.evidence[sessionID] == nil {
		s.evidence[sessionID] = make(map[string]memoryEvidenceRow)
	}
	return s.evidence[sessionID]
}

func (s *Memory) LoadLedger(ctx context.Context, sessionID string) (evidence.Ledger, error) {
	if err := ctx.Err(); err != nil {
		return evidence.Ledger{}, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return evidence.Ledger{}, fmt.Errorf("session: empty session id")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.sessions[sessionID]; !ok {
		return evidence.Ledger{}, ErrSessionNotFound
	}
	rows := s.evidence[sessionID]
	var records []evidence.Record
	for handle, row := range rows {
		rec := row.rec
		rec.SupersededBy = row.supersededBy
		rec.Handle = handle
		records = append(records, rec)
	}
	return evidence.AssembleLedger(records), nil
}

func (s *Memory) UpsertEvidenceRecord(ctx context.Context, sessionID string, rec evidence.Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	handle := strings.TrimSpace(rec.Handle)
	if handle == "" {
		return fmt.Errorf("session: upsert evidence requires handle")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[sessionID]; !ok {
		return ErrSessionNotFound
	}
	ledger := s.ensureEvidenceSession(sessionID)
	if _, exists := ledger[handle]; exists {
		return nil
	}
	ledger[handle] = memoryEvidenceRow{rec: rec}
	s.bumpUntrustedLocked(sessionID, rec)
	s.bumpSecretExposureLocked(sessionID, rec)
	return nil
}

func (s *Memory) MarkSuperseded(ctx context.Context, sessionID, oldHandle, byHandle string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	oldHandle = strings.TrimSpace(oldHandle)
	byHandle = strings.TrimSpace(byHandle)
	if oldHandle == "" || byHandle == "" || oldHandle == byHandle {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger := s.evidence[sessionID]
	row, ok := ledger[oldHandle]
	if !ok || row.supersededBy != "" {
		return nil
	}
	row.supersededBy = byHandle
	ledger[oldHandle] = row
	return nil
}

func (s *Memory) nextEvidenceOrdinalLocked(sessionID, kind string) int {
	max := 0
	for handle := range s.evidence[sessionID] {
		k, ord := evidence.ParseHandleOrdinal(handle)
		if k == kind && ord > max {
			max = ord
		}
	}
	return max + 1
}

func (s *Memory) CommitEvidenceToolResult(ctx context.Context, sessionID, projectDir, toolName string, args map[string]any, content string) (string, string, error) {
	return s.commitEvidenceToolResult(ctx, sessionID, projectDir, toolName, args, content, "")
}

// CommitVisualEvidenceToolResult also stamps the minted handle onto the
// result's stored artifact before the ledger row becomes visible.
func (s *Memory) CommitVisualEvidenceToolResult(ctx context.Context, sessionID, projectDir, toolName string, args map[string]any, content, artifactID string) (string, string, error) {
	return s.commitEvidenceToolResult(ctx, sessionID, projectDir, toolName, args, content, strings.TrimSpace(artifactID))
}

// SetArtifactHandleBinder lets evidence commits stamp handles onto artifacts.
func (s *Memory) SetArtifactHandleBinder(binder visual.HandleBinder) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.artifactHandles = binder
}

func (s *Memory) commitEvidenceToolResult(ctx context.Context, sessionID, projectDir, toolName string, args map[string]any, content, artifactID string) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	rec := evidence.BuildEvidenceRecord(projectDir, toolName, args, content)
	if rec.Kind == "" {
		return "", content, nil
	}
	rec.SourceTool = strings.TrimSpace(toolName)
	rec.ArtifactID = artifactID

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[sessionID]; !ok {
		return "", "", ErrSessionNotFound
	}
	liveRows := s.ensureEvidenceSession(sessionID)
	var liveRecords []evidence.Record
	for handle, row := range liveRows {
		if row.supersededBy != "" {
			continue
		}
		recCopy := row.rec
		recCopy.Handle = handle
		liveRecords = append(liveRecords, recCopy)
	}
	live := evidence.AssembleLedger(liveRecords)

	ordinal := s.nextEvidenceOrdinalLocked(sessionID, rec.Kind)

	patched := content
	var highlightRecs []evidence.Record
	var handle string
	if strings.EqualFold(toolName, "summarize") {
		handle, patched, highlightRecs = evidenceSummarizeCommit(projectDir, content, rec, ordinal)
	} else if strings.EqualFold(toolName, "capture_page") && len(evidence.CaptureFrameHighlightRecords(content)) > 0 {
		handle, patched, highlightRecs = evidenceCaptureFilmstripCommit(content, rec, ordinal)
	} else {
		handle = evidence.FormatHandle(rec.Kind, ordinal)
		if strings.EqualFold(toolName, "read") || strings.EqualFold(toolName, "list_dir") ||
			strings.EqualFold(toolName, "grep") || strings.EqualFold(toolName, "find") {
			ordinalCursor := map[string]int{rec.Kind: ordinal}
			nextOrdinal := func(kind string) int {
				ordinalCursor[kind]++
				return ordinalCursor[kind]
			}
			patched, highlightRecs = mintToolHighlightRecords(projectDir, toolName, content, nextOrdinal)
		}
	}
	rec.Handle = handle
	if artifactID != "" {
		if s.artifactHandles == nil {
			return "", content, fmt.Errorf("bind %s evidence to artifact %s: artifact handle binder not configured", toolName, artifactID)
		}
		if err := s.artifactHandles.BindEvidenceHandle(ctx, artifactID, handle); err != nil {
			return "", content, err
		}
	}

	for _, path := range evidence.IndexedPathsForRecord(rec) {
		for _, oldHandle := range live.ByPath[path] {
			if oldHandle == handle {
				continue
			}
			row, ok := liveRows[oldHandle]
			if !ok || row.supersededBy != "" {
				continue
			}
			if !evidence.SupersedesPathHandle(rec, row.rec) {
				continue
			}
			row.supersededBy = handle
			liveRows[oldHandle] = row
		}
	}
	liveRows[handle] = memoryEvidenceRow{rec: rec}
	s.bumpUntrustedLocked(sessionID, rec)
	s.bumpSecretExposureLocked(sessionID, rec)

	for _, hrec := range highlightRecs {
		liveRows[hrec.Handle] = memoryEvidenceRow{rec: hrec}
		s.bumpUntrustedLocked(sessionID, hrec)
		s.bumpSecretExposureLocked(sessionID, hrec)
	}
	if strings.EqualFold(toolName, "read") {
		if marker, ok := secretExposureReadRecord(evidencePathArg(args)); ok {
			if _, exists := liveRows[marker.Handle]; !exists {
				liveRows[marker.Handle] = memoryEvidenceRow{rec: marker}
				s.bumpSecretExposureLocked(sessionID, marker)
			}
		}
	}
	return handle, patched, nil
}
