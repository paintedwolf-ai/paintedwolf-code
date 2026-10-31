package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/bloblifecycle"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/evidence"
)

func nextEvidenceOrdinal(ctx context.Context, q *db.Queries, sessionID, kind string) (int, error) {
	kind = strings.TrimSpace(strings.ToLower(kind))
	if kind == "" {
		return 0, fmt.Errorf("session: empty evidence kind")
	}
	next, err := q.NextEvidenceOrdinalForKind(ctx, db.NextEvidenceOrdinalForKindParams{
		SessionID: sessionID,
		Kind:      kind,
	})
	if err != nil {
		return 0, fmt.Errorf("next evidence ordinal: %w", err)
	}
	return int(next), nil
}

func supersedePathsForRecord(ctx context.Context, q *db.Queries, sessionID, newHandle string, rec evidence.Record, live evidence.Ledger) error {
	for _, path := range evidence.IndexedPathsForRecord(rec) {
		for _, oldHandle := range live.ByPath[path] {
			if oldHandle == newHandle {
				continue
			}
			oldRec, ok := live.Handles[oldHandle]
			if !ok || !evidence.SupersedesPathHandle(rec, oldRec) {
				continue
			}
			if err := markSuperseded(ctx, q, sessionID, oldHandle, newHandle); err != nil {
				return err
			}
		}
	}
	return nil
}

// loadFileBindings reads live path metadata without loading body blobs.
func loadFileBindings(ctx context.Context, q *db.Queries, sessionID string, incoming evidence.Record) (evidence.Ledger, error) {
	if !evidence.UpdatesFileBindings(incoming) {
		return evidence.InitLedger(), nil
	}
	rows, err := q.ListEvidenceRecords(ctx, sessionID)
	if err != nil {
		return evidence.Ledger{}, err
	}
	var records []evidence.Record
	for _, row := range rows {
		if row.SupersededBy.Valid {
			continue
		}
		rec := evidence.Record{Handle: row.Handle, Kind: row.Kind, Shape: row.Shape, Path: row.Path, Survey: row.Survey != 0}
		if err := unmarshalEvidenceLineRanges(row.LineRanges, &rec); err != nil {
			return evidence.Ledger{}, fmt.Errorf("decode path binding %s: %w", rec.Handle, err)
		}
		if !evidence.SupersedesPathHandle(incoming, rec) {
			continue
		}
		records = append(records, rec)
	}
	return evidence.AssembleLedger(records), nil
}

// CommitEvidenceToolResult persists the observation and updates file bindings.
func (ev *sqlEvidence) CommitEvidenceToolResult(ctx context.Context, sessionID, projectDir, toolName string, args map[string]any, content string) (string, string, error) {
	return commitEvidenceToolResultSQL(ctx, ev.core, sessionID, projectDir, toolName, args, content, "")
}

// CommitVisualEvidenceToolResult also stamps the minted handle onto the
// result's stored artifact in the same transaction.
func (ev *sqlEvidence) CommitVisualEvidenceToolResult(ctx context.Context, sessionID, projectDir, toolName string, args map[string]any, content, artifactID string) (string, string, error) {
	return commitEvidenceToolResultSQL(ctx, ev.core, sessionID, projectDir, toolName, args, content, strings.TrimSpace(artifactID))
}

func commitEvidenceToolResultSQL(ctx context.Context, s *sqlCore, sessionID, projectDir, toolName string, args map[string]any, content, artifactID string) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", content, err
	}
	rec := evidence.BuildEvidenceRecord(projectDir, toolName, args, content)
	if rec.Kind == "" {
		return "", content, nil
	}
	rec.SourceTool = strings.TrimSpace(toolName)
	rec.ArtifactID = artifactID
	if artifactID != "" && s.artifacts == nil {
		return "", content, fmt.Errorf("bind %s evidence to artifact %s: artifact records not configured", toolName, artifactID)
	}

	release := bloblifecycle.AcquirePublication(s.dataDir)
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", content, err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.queries.WithTx(tx)
	projectID, err := qtx.GetSessionProjectID(ctx, sessionID)
	if err != nil {
		return "", content, fmt.Errorf("resolve evidence record project: %w", err)
	}

	live, err := loadFileBindings(ctx, qtx, sessionID, rec)
	if err != nil {
		return "", content, err
	}
	ordinal, err := nextEvidenceOrdinal(ctx, qtx, sessionID, rec.Kind)
	if err != nil {
		return "", content, err
	}

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

	if err := supersedePathsForRecord(ctx, qtx, sessionID, handle, rec, live); err != nil {
		return "", content, err
	}
	inserted, err := upsertEvidenceRecord(ctx, s, qtx, sessionID, projectID, rec)
	if err != nil {
		return "", content, err
	}
	if inserted {
		bumpUntrusted(s, sessionID, rec)
		bumpSecretExposure(s, sessionID, rec)
	}

	for _, hrec := range highlightRecs {
		hin, err := upsertEvidenceRecord(ctx, s, qtx, sessionID, projectID, hrec)
		if err != nil {
			return "", content, err
		}
		if hin {
			bumpUntrusted(s, sessionID, hrec)
			bumpSecretExposure(s, sessionID, hrec)
		}
	}
	if strings.EqualFold(toolName, "read") {
		if marker, ok := secretExposureReadRecord(evidencePathArg(args)); ok {
			inserted, err := upsertEvidenceRecord(ctx, s, qtx, sessionID, projectID, marker)
			if err != nil {
				return "", content, err
			}
			if inserted {
				bumpSecretExposure(s, sessionID, marker)
			}
		}
	}

	if artifactID != "" {
		if err := s.artifacts.BindEvidenceHandleTx(ctx, tx, projectID, artifactID, handle); err != nil {
			return "", content, err
		}
	}

	if err := tx.Commit(); err != nil {
		return "", content, err
	}
	if artifactID != "" {
		s.artifacts.EvidenceHandleBound(ctx, artifactID, handle)
	}
	return handle, patched, nil
}
