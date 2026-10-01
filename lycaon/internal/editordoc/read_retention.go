package editordoc

import (
	"context"
	"database/sql"
)

const readDocumentBytes = 64 << 20
const readDocumentCount = 64

// trimReadDocuments evicts clean, unjoined documents past the read budget, least
// recently opened first; joined documents keep their identities for disconnected replicas.
func (s *Service) trimReadDocuments(ctx context.Context, keep string) error {
	s.ops.Lock()
	defer s.ops.Unlock()
	rows, err := s.store.db.QueryContext(ctx, `SELECT d.id,
 length(CAST(d.draft AS BLOB))+length(CAST(d.base_content AS BLOB))+length(h.checkpoint)+length(h.published_checkpoint)
 +COALESCE((SELECT SUM(length(payload)) FROM editor_document_snapshots WHERE document_id=d.id),0)
 FROM editor_documents d JOIN editor_replica_heads h ON h.document_id=d.id
 WHERE d.dirty=0 AND d.diverged=0 AND d.held_agent_version_id=''
 AND NOT EXISTS (SELECT 1 FROM editor_document_changes c WHERE c.document_id=d.id)
 AND NOT EXISTS (SELECT 1 FROM editor_agent_receipts a WHERE a.document_id=d.id)
 AND NOT EXISTS (SELECT 1 FROM editor_replica_receipts r WHERE r.document_id=d.id AND r.command_history IS NOT NULL)
 AND NOT EXISTS (SELECT 1 FROM source_text_contributions c WHERE c.document_id=d.id AND c.revision>1 AND c.origin!='external')
 AND NOT EXISTS (SELECT 1 FROM editor_document_retention t WHERE t.document_id=d.id)
 AND NOT EXISTS (SELECT 1 FROM editor_replicas r WHERE r.document_id=d.id)
 AND NOT EXISTS (SELECT 1 FROM editor_save_pins p WHERE p.document_id=d.id)
 AND NOT EXISTS (SELECT 1 FROM editor_mutations m WHERE m.document_id=d.id)
 AND NOT EXISTS (SELECT 1 FROM source_version_text_states v WHERE v.document_id=d.id)
 ORDER BY (d.id=?) DESC,d.opened_at DESC,d.id`, keep)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var victims []string
	var bytes, count int64
	for rows.Next() {
		var id string
		var size int64
		if err := rows.Scan(&id, &size); err != nil {
			return err
		}
		s.presenceMu.Lock()
		active := len(s.participants[id]) > 0
		s.presenceMu.Unlock()
		if active {
			continue
		}
		bytes += size
		count++
		if id != keep && (bytes > readDocumentBytes || count > readDocumentCount) {
			victims = append(victims, id)
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, id := range victims {
		if err := s.store.Tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `DELETE FROM source_text_contributions WHERE document_id=?`, id); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `DELETE FROM editor_documents WHERE id=?`, id)
			return err
		}); err != nil {
			return err
		}
		s.replicas.mu.Lock()
		s.replicas.evict(ctx, id)
		s.replicas.mu.Unlock()
		s.agentReads.mu.Lock()
		for key := range s.agentReads.bases {
			if key.document == id {
				delete(s.agentReads.bases, key)
			}
		}
		s.agentReads.mu.Unlock()
	}
	return nil
}
