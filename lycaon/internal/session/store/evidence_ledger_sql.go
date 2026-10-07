package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/bloblifecycle"
	"github.com/lycaon/lycaon/internal/contentblob"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/search"
)

type storedLineRangesPayload struct {
	Version      int                       `json:"v"`
	Ranges       []evidence.LineRange      `json:"ranges,omitempty"`
	PathsTouched []string                  `json:"paths_touched,omitempty"`
	URLsTouched  []string                  `json:"urls_touched,omitempty"`
	URLTitles    map[string]string         `json:"url_titles,omitempty"`
	GrepLines    map[string]map[int]string `json:"grep_lines,omitempty"`
	PathTiers    map[string]string         `json:"path_tiers,omitempty"`
	Surface      string                    `json:"surface,omitempty"`
	ArtifactID   string                    `json:"artifact_id,omitempty"`
	FrameIndex   int                       `json:"frame_index,omitempty"`
}

func marshalEvidenceLineRanges(rec evidence.Record) (string, error) {
	aux := evidence.CloneRecordForStore(rec)
	payload := storedLineRangesPayload{
		Version:      1,
		Ranges:       rec.LineRanges,
		PathsTouched: aux.PathsTouched,
		URLsTouched:  aux.URLsTouched,
		URLTitles:    aux.URLTitles,
		GrepLines:    aux.GrepLines,
		PathTiers:    aux.PathTiers,
		Surface:      rec.Surface,
		ArtifactID:   rec.ArtifactID,
		FrameIndex:   rec.FrameIndex,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func unmarshalEvidenceLineRanges(raw string, rec *evidence.Record) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("missing evidence record payload")
	}
	var payload storedLineRangesPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return err
	}
	if payload.Version != 1 {
		return fmt.Errorf("unsupported evidence record payload version %d", payload.Version)
	}
	rec.LineRanges = payload.Ranges
	evidence.ApplyRecordAux(rec, evidence.RecordAux{
		PathsTouched: payload.PathsTouched,
		URLsTouched:  payload.URLsTouched,
		URLTitles:    payload.URLTitles,
		GrepLines:    payload.GrepLines,
		PathTiers:    payload.PathTiers,
	})
	rec.Surface = payload.Surface
	rec.ArtifactID = payload.ArtifactID
	rec.FrameIndex = payload.FrameIndex
	return nil
}

// marshalEvidenceBody stores captured bodies by digest in the caller's transaction.
func (s *SQL) marshalEvidenceBody(ctx context.Context, q *db.Queries, projectID string, rec evidence.Record) (string, error) {
	if len(rec.Body) == 0 {
		return "", nil
	}
	raw, err := json.Marshal(rec.Body)
	if err != nil {
		return "", fmt.Errorf("marshal evidence body: %w", err)
	}
	blobStore := contentblob.StoreFor(s.dataDir, projectID)
	sha, byteSize, storedSize, err := contentblob.Write(blobStore, raw)
	if err != nil {
		return "", fmt.Errorf("write evidence body blob: %w", err)
	}
	if err := q.UpsertContentBlobObject(ctx, db.UpsertContentBlobObjectParams{
		ProjectID: projectID, Sha256: sha, ByteSize: byteSize, StoredSize: storedSize,
		CreatedAt: db.FormatTime(time.Now().UTC()),
	}); err != nil {
		return "", fmt.Errorf("upsert content blob object: %w", err)
	}
	return sha, nil
}

// unmarshalEvidenceBody restores rec.Body from its content blob. Missing blob files
// are tolerated so lost files do not break later turns.
func (s *SQL) unmarshalEvidenceBody(projectID, sha string, rec *evidence.Record) error {
	sha = strings.TrimSpace(sha)
	if sha == "" {
		return nil
	}
	blobStore := contentblob.StoreFor(s.dataDir, projectID)
	raw, err := contentblob.Read(blobStore, sha)
	if errors.Is(err, contentblob.ErrMissing) {
		if _, reported := s.lostBodies.LoadOrStore(sha, struct{}{}); !reported {
			slog.Error("evidence body file is missing; the record loads without its body",
				"project_id", projectID, "handle", rec.Handle, "sha256", sha)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read evidence body blob: %w", err)
	}
	return json.Unmarshal(raw, &rec.Body)
}

func evidenceOrdinalFromHandle(handle string) int {
	_, ord := evidence.ParseHandleOrdinal(handle)
	return ord
}

func (s *SQL) LoadLedger(ctx context.Context, sessionID string) (evidence.Ledger, error) {
	return s.loadLedger(ctx, s.queries, sessionID)
}

func (s *SQL) loadLedger(ctx context.Context, q *db.Queries, sessionID string) (evidence.Ledger, error) {
	if err := ctx.Err(); err != nil {
		return evidence.Ledger{}, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return evidence.Ledger{}, fmt.Errorf("session: empty session id")
	}
	rows, err := q.ListEvidenceRecords(ctx, sessionID)
	if err != nil {
		return evidence.Ledger{}, fmt.Errorf("load evidence ledger: %w", err)
	}
	records := make([]evidence.Record, 0, len(rows))
	for _, r := range rows {
		rec := evidence.Record{
			Handle:       r.Handle,
			SupersededBy: r.SupersededBy.String,
			Kind:         r.Kind,
			Shape:        r.Shape,
			Fidelity:     r.Fidelity,
			SourceTool:   r.SourceTool,
			Path:         r.Path,
			URL:          r.Url,
			Truncated:    r.Truncated != 0,
			Survey:       r.Survey != 0,
		}
		if err := unmarshalEvidenceLineRanges(r.LineRanges, &rec); err != nil {
			return evidence.Ledger{}, fmt.Errorf("decode line_ranges for %s: %w", rec.Handle, err)
		}
		if err := s.unmarshalEvidenceBody(r.ProjectID, r.ContentBlobSha256, &rec); err != nil {
			return evidence.Ledger{}, fmt.Errorf("decode body for %s: %w", rec.Handle, err)
		}
		records = append(records, rec)
	}
	return evidence.AssembleLedger(records), nil
}

func (s *SQL) UpsertEvidenceRecord(ctx context.Context, sessionID string, rec evidence.Record) error {
	release := bloblifecycle.AcquirePublication(s.dataDir)
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.queries.WithTx(tx)
	projectID, err := qtx.GetSessionProjectID(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("resolve evidence record project: %w", err)
	}
	inserted, err := s.upsertEvidenceRecord(ctx, qtx, sessionID, projectID, rec)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if inserted {
		s.bumpUntrusted(sessionID, rec)
		s.bumpSecretExposure(sessionID, rec)
		if err := s.projectUntrustedLedger(ctx, nil, sessionID, rec); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQL) upsertEvidenceRecord(ctx context.Context, q *db.Queries, sessionID, projectID string, rec evidence.Record) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	sessionID = strings.TrimSpace(sessionID)
	handle := strings.TrimSpace(rec.Handle)
	if sessionID == "" || handle == "" {
		return false, fmt.Errorf("session: upsert evidence requires session_id and handle")
	}
	lineRanges, err := marshalEvidenceLineRanges(rec)
	if err != nil {
		return false, err
	}
	sha, err := s.marshalEvidenceBody(ctx, q, projectID, rec)
	if err != nil {
		return false, err
	}
	ordinal := evidenceOrdinalFromHandle(handle)
	truncated := 0
	if rec.Truncated {
		truncated = 1
	}
	survey := 0
	if rec.Survey {
		survey = 1
	}
	n, err := q.InsertEvidenceRecordIfAbsent(ctx, db.InsertEvidenceRecordIfAbsentParams{
		SessionID:         sessionID,
		ProjectID:         projectID,
		Handle:            handle,
		Ordinal:           int64(ordinal),
		Kind:              rec.Kind,
		Shape:             rec.Shape,
		Fidelity:          rec.Fidelity,
		SourceTool:        rec.SourceTool,
		MarksUntrusted:    boolInt(evidence.RecordMarksUntrustedContent(rec)),
		Path:              rec.Path,
		Url:               rec.URL,
		LineRanges:        lineRanges,
		ContentBlobSha256: sha,
		Truncated:         int64(truncated),
		Survey:            int64(survey),
	})
	if err != nil {
		return false, fmt.Errorf("upsert evidence record: %w", err)
	}
	return n > 0, nil
}

// projectUntrustedLedger projects inherited and worker URL markers.
// A nil transaction uses a short local transaction.
func (s *SQL) projectUntrustedLedger(ctx context.Context, tx *sql.Tx, sessionID string, rec evidence.Record) error {
	if !evidence.RecordMarksUntrustedContent(rec) {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	q := s.queries
	if tx != nil {
		q = s.queries.WithTx(tx)
	}
	projectID, err := q.GetSessionProjectID(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("project untrusted ledger: session %s: %w", sessionID, err)
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil
	}
	if tx != nil {
		return search.SyncUntrustedLedgerWriteThrough(ctx, tx, projectID, sessionID, rec)
	}
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := search.SyncUntrustedLedgerWriteThrough(ctx, tx, projectID, sessionID, rec); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQL) MarkSuperseded(ctx context.Context, sessionID, oldHandle, byHandle string) error {
	return markSuperseded(ctx, s.queries, sessionID, oldHandle, byHandle)
}

func markSuperseded(ctx context.Context, q *db.Queries, sessionID, oldHandle, byHandle string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sessionID = strings.TrimSpace(sessionID)
	oldHandle = strings.TrimSpace(oldHandle)
	byHandle = strings.TrimSpace(byHandle)
	if sessionID == "" || oldHandle == "" || byHandle == "" {
		return nil
	}
	if err := q.MarkEvidenceRecordSuperseded(ctx, db.MarkEvidenceRecordSupersededParams{
		SupersededBy: db.NullString(byHandle),
		SessionID:    sessionID,
		Handle:       oldHandle,
		Handle_2:     byHandle,
	}); err != nil {
		return fmt.Errorf("mark evidence superseded: %w", err)
	}
	return nil
}

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

func (s *SQL) supersedePathsForRecord(ctx context.Context, q *db.Queries, sessionID, newHandle string, rec evidence.Record, live evidence.Ledger) error {
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
func (s *SQL) CommitEvidenceToolResult(ctx context.Context, sessionID, projectDir, toolName string, args map[string]any, content string) (string, string, error) {
	return s.commitEvidenceToolResult(ctx, sessionID, projectDir, toolName, args, content, "")
}

// CommitVisualEvidenceToolResult also stamps the minted handle onto the
// result's stored artifact in the same transaction.
func (s *SQL) CommitVisualEvidenceToolResult(ctx context.Context, sessionID, projectDir, toolName string, args map[string]any, content, artifactID string) (string, string, error) {
	return s.commitEvidenceToolResult(ctx, sessionID, projectDir, toolName, args, content, strings.TrimSpace(artifactID))
}

func (s *SQL) commitEvidenceToolResult(ctx context.Context, sessionID, projectDir, toolName string, args map[string]any, content, artifactID string) (string, string, error) {
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

	if err := s.supersedePathsForRecord(ctx, qtx, sessionID, handle, rec, live); err != nil {
		return "", content, err
	}
	inserted, err := s.upsertEvidenceRecord(ctx, qtx, sessionID, projectID, rec)
	if err != nil {
		return "", content, err
	}
	if inserted {
		s.bumpUntrusted(sessionID, rec)
		s.bumpSecretExposure(sessionID, rec)
	}

	for _, hrec := range highlightRecs {
		hin, err := s.upsertEvidenceRecord(ctx, qtx, sessionID, projectID, hrec)
		if err != nil {
			return "", content, err
		}
		if hin {
			s.bumpUntrusted(sessionID, hrec)
			s.bumpSecretExposure(sessionID, hrec)
		}
	}
	if strings.EqualFold(toolName, "read") {
		if marker, ok := secretExposureReadRecord(evidencePathArg(args)); ok {
			inserted, err := s.upsertEvidenceRecord(ctx, qtx, sessionID, projectID, marker)
			if err != nil {
				return "", content, err
			}
			if inserted {
				s.bumpSecretExposure(sessionID, marker)
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
		s.artifacts.EvidenceHandleBound(artifactID, handle)
	}
	return handle, patched, nil
}
