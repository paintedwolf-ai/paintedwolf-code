package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/contentblob"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/evidence"
)

// writeEvidenceRecord is the complete typed record/body publication shared by
// legacy tools and exact source delivery. The caller owns the transaction and
// publication lease; bodies and reference metadata use the same existing stores.
func writeEvidenceRecord(ctx context.Context, q *db.Queries, dataDir, sessionID, projectID string, rec evidence.Record) (bool, error) {
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
	sha, err := marshalEvidenceBody(ctx, q, dataDir, projectID, rec)
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

// marshalEvidenceBody stores captured bodies by digest in the caller's transaction.
func marshalEvidenceBody(ctx context.Context, q *db.Queries, dataDir, projectID string, rec evidence.Record) (string, error) {
	if len(rec.Body) == 0 {
		return "", nil
	}
	raw, err := json.Marshal(rec.Body)
	if err != nil {
		return "", fmt.Errorf("marshal evidence body: %w", err)
	}
	blobStore := contentblob.StoreFor(dataDir, projectID)
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
