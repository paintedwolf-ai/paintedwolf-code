package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/bloblifecycle"
	"github.com/lycaon/lycaon/internal/contentblob"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/search"
)

type storedLineRangesPayload struct {
	Version          int                       `json:"v"`
	SourceDelivery   *evidence.SourceDelivery  `json:"source_delivery,omitempty"`
	Ranges           []evidence.LineRange      `json:"ranges,omitempty"`
	PathsTouched     []string                  `json:"paths_touched,omitempty"`
	URLsTouched      []string                  `json:"urls_touched,omitempty"`
	URLTitles        map[string]string         `json:"url_titles,omitempty"`
	GrepLines        map[string]map[int]string `json:"grep_lines,omitempty"`
	PathTiers        map[string]string         `json:"path_tiers,omitempty"`
	Surface          string                    `json:"surface,omitempty"`
	ArtifactID       string                    `json:"artifact_id,omitempty"`
	FrameIndex       int                       `json:"frame_index,omitempty"`
	UntrustedContent bool                      `json:"untrusted_content,omitempty"`
	ExternalCustody  bool                      `json:"external_custody,omitempty"`
	CustodySeq       int                       `json:"custody_seq,omitempty"`
}

func marshalEvidenceLineRanges(rec evidence.Record) (string, error) {
	aux := evidence.CloneRecordForStore(rec)
	payload := storedLineRangesPayload{
		Version:          1,
		SourceDelivery:   rec.SourceDelivery,
		Ranges:           rec.LineRanges,
		PathsTouched:     aux.PathsTouched,
		URLsTouched:      aux.URLsTouched,
		URLTitles:        aux.URLTitles,
		GrepLines:        aux.GrepLines,
		PathTiers:        aux.PathTiers,
		Surface:          rec.Surface,
		ArtifactID:       rec.ArtifactID,
		FrameIndex:       rec.FrameIndex,
		UntrustedContent: aux.UntrustedContent,
		ExternalCustody:  aux.ExternalCustody,
		CustodySeq:       aux.CustodySeq,
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
	rec.SourceDelivery = payload.SourceDelivery
	evidence.ApplyRecordAux(rec, evidence.RecordAux{
		PathsTouched:     payload.PathsTouched,
		URLsTouched:      payload.URLsTouched,
		URLTitles:        payload.URLTitles,
		GrepLines:        payload.GrepLines,
		PathTiers:        payload.PathTiers,
		UntrustedContent: payload.UntrustedContent,
		ExternalCustody:  payload.ExternalCustody,
		CustodySeq:       payload.CustodySeq,
	})
	rec.Surface = payload.Surface
	rec.ArtifactID = payload.ArtifactID
	rec.FrameIndex = payload.FrameIndex
	return nil
}

// unmarshalEvidenceBody restores rec.Body from its content blob. Missing blob files
// are tolerated so lost files do not break later turns.
func (s *sqlCore) unmarshalEvidenceBody(projectID, sha string, rec *evidence.Record) error {
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

func (ev *sqlEvidence) LoadLedger(ctx context.Context, sessionID string) (evidence.Ledger, error) {
	return loadLedger(ctx, ev.core, ev.core.queries, sessionID)
}

func loadLedger(ctx context.Context, s *sqlCore, q *db.Queries, sessionID string) (evidence.Ledger, error) {
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

func (ev *sqlEvidence) UpsertEvidenceRecord(ctx context.Context, sessionID string, rec evidence.Record) error {
	return upsertEvidenceRecordSQL(ctx, ev.core, sessionID, rec)
}

func upsertEvidenceRecordSQL(ctx context.Context, s *sqlCore, sessionID string, rec evidence.Record) error {
	unlock := s.lockMutation(sessionID)
	defer unlock()
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
	if evidence.ReplacesWholeFileCustody(rec) && rec.CustodySeq() == 0 {
		seq, err := nextCustodySeq(ctx, qtx, sessionID)
		if err != nil {
			return err
		}
		rec.SetCustodySeq(seq)
	}
	inserted, err := writeEvidenceRecord(ctx, qtx, s.dataDir, sessionID, projectID, rec)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if inserted {
		bumpUntrusted(s, sessionID, rec)
		bumpSecretExposure(s, sessionID, rec)
		if err := s.projectUntrustedLedger(ctx, nil, sessionID, rec); err != nil {
			return err
		}
	}
	return nil
}

// projectUntrustedLedger projects inherited and worker URL markers.
// A nil transaction uses a short local transaction.
func (s *sqlCore) projectUntrustedLedger(ctx context.Context, tx *sql.Tx, sessionID string, rec evidence.Record) error {
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

func (ev *sqlEvidence) MarkSuperseded(ctx context.Context, sessionID, oldHandle, byHandle string) error {
	return markSuperseded(ctx, ev.core.queries, sessionID, oldHandle, byHandle)
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
