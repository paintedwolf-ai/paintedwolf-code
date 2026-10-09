package historyretention

import (
	"context"

	"github.com/lycaon/lycaon/pkg/api"
)

// Class content counts include retained and protected bodies, not only prune candidates.
func (s *Service) classUsage(ctx context.Context) ([]api.HistoryClassUsage, error) {
	queries := []struct{ class, query string }{
		{"recordings", `SELECT COALESCE(SUM(bytes),0) FROM (SELECT MAX(byte_size) bytes FROM artifacts WHERE retention_class='recording' AND deleted_at IS NULL GROUP BY project_id,content_hash)`},
		{"checkpoints", `SELECT COALESCE(SUM(size),0) FROM source_blob_objects b WHERE EXISTS(SELECT 1 FROM checkpoint_object_refs r WHERE r.sha256=b.sha256)`},
		{"source_revisions", `SELECT COALESCE(SUM(size),0) FROM source_blob_objects b WHERE EXISTS(SELECT 1 FROM source_versions v WHERE v.content_sha256=b.sha256 AND v.capture_state='stored')`},
		{"scan_detail", `SELECT COALESCE(SUM(COALESCE(length(CAST(result_json AS BLOB)),0)+COALESCE(length(CAST(ingest_json AS BLOB)),0)+COALESCE(length(CAST(guidance_json AS BLOB)),0)),0) FROM code_scans`},
		{"receipt_detail", `SELECT COALESCE(SUM(COALESCE(length(CAST(rate_snapshot AS BLOB)),0)+COALESCE(length(CAST(provider_id AS BLOB)),0)+COALESCE(length(CAST(model AS BLOB)),0)+COALESCE(length(CAST(caller AS BLOB)),0)),0) FROM llm_calls`},
	}
	out := make([]api.HistoryClassUsage, 0, len(queries))
	for _, item := range queries {
		value := api.HistoryClassUsage{Class: item.class}
		if err := s.Database.QueryRowContext(ctx, item.query).Scan(&value.ContentBytes); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, nil
}
