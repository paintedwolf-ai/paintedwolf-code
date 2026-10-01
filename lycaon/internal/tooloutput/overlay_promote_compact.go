package tooloutput

import (
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

// InlineOverlayPromoteJSON strips full conflict bodies when a digest is present or buildable,
// except for small single-path previews.
func InlineOverlayPromoteJSON(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw[0] != '{' {
		return raw, false
	}
	jsonPart := raw
	if idx := strings.Index(raw, "\n"+hostmarker.Rejected); idx > 0 {
		jsonPart = raw[:idx]
	}
	var out api.WorkerMergeResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(jsonPart)), &out); err != nil {
		return raw, false
	}
	if len(out.Conflicts) == 0 && len(out.ConflictDigest) == 0 {
		return raw, false
	}
	if ScopedPathInlineHunks(out) {
		return raw, false
	}
	if len(out.ConflictDigest) == 0 {
		out.ConflictDigest = BuildConflictDigest(out.Conflicts)
	} else {
		out.ConflictDigest = rebuildStoredDigest(out.ConflictDigest)
	}
	if len(out.ConflictDigest) == 0 {
		return raw, false
	}
	out.Conflicts = nil
	compact, err := surveyjson.Marshal(out)
	if err != nil {
		return raw, false
	}
	if idx := strings.Index(raw, "\n"+hostmarker.Rejected); idx > 0 {
		return string(compact) + raw[idx:], true
	}
	return string(compact), true
}

// ScopedPathInlineHunks reports whether one path keeps inline conflict hunks.
func ScopedPathInlineHunks(out api.WorkerMergeResult) bool {
	if len(out.Paths) != 1 || len(out.Conflicts) != 1 {
		return false
	}
	path := strings.TrimSpace(out.Paths[0])
	conflict := out.Conflicts[0]
	if strings.TrimSpace(conflict.Path) != path {
		return false
	}
	hunkCount := len(conflict.Hunks)
	if hunkCount <= 0 {
		for _, row := range out.PathStatus {
			if row.Path == path && row.Status == api.WorkerPromotePathOutcomeConflict {
				hunkCount = row.HunkCount
				break
			}
		}
	}
	return hunkCount > 0 && hunkCount <= PromoteScopedPathInlineHunkThreshold
}

func rebuildStoredDigest(rows []api.WorkerPromoteConflictDigest) []api.WorkerPromoteConflictDigest {
	if len(rows) == 0 {
		return nil
	}
	out := make([]api.WorkerPromoteConflictDigest, 0, len(rows))
	for _, row := range rows {
		out = append(out, BuildConflictDigestRow(row.Path, row.Summary, nil, row.BranchDelta, row.BaseDelta, row.ConflictTier))
	}
	return out
}
