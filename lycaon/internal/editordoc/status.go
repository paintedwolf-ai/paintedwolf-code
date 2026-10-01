package editordoc

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/lycaon/lycaon/pkg/api"
)

// Statuses reads accepted metadata without allocating text or a CRDT runtime.
func (s *Service) Statuses(ctx context.Context, projectID string, ids []string) (api.EditorDocumentStatuses, error) {
	result := api.EditorDocumentStatuses{Documents: []api.EditorDocumentStatus{}, Missing: []string{}}
	if len(ids) == 0 || len(ids) > 64 {
		return result, errors.New("document status reads require between 1 and 64 identities")
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return result, err
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT d.id,d.file_id,d.root_id,d.path,d.branch_id,d.revision,h.epoch,h.published_revision,
 d.dirty,d.diverged,d.absent,d.base_sha256,d.size_bytes,d.encoding,d.eol,d.base_eol,d.mixed_eol,d.base_mixed_eol,d.held_agent_version_id
 FROM editor_documents d JOIN editor_replica_heads h ON h.document_id=d.id
 WHERE d.project_id=? AND d.id IN (SELECT value FROM json_each(?)) ORDER BY d.id`, projectID, string(raw))
	if err != nil {
		return result, err
	}
	defer func() { _ = rows.Close() }()
	found := make(map[string]bool, len(ids))
	for rows.Next() {
		var d api.EditorDocumentStatus
		if err := rows.Scan(&d.ID, &d.FileID, &d.RootID, &d.Path, &d.BranchID, &d.Revision, &d.Epoch, &d.PublishedRevision,
			&d.Dirty, &d.Diverged, &d.Absent, &d.BaseSHA256, &d.SizeBytes, &d.Encoding, &d.EOL, &d.BaseEOL, &d.MixedEOL, &d.BaseMixedEOL, &d.HeldAgentVersionID); err != nil {
			return result, err
		}
		found[d.ID] = true
		result.Documents = append(result.Documents, d)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	for _, id := range ids {
		if !found[id] {
			result.Missing = append(result.Missing, id)
			found[id] = true
		}
	}
	return result, nil
}
