package compaction

import (
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	overlayPromoteChunkPreservePrefix = hostmarker.CompactionBannerOpen +
		"overlay promote — hunk bodies spilled; branch_delta kept inline" +
		hostmarker.CompactionBannerClose + "\n\n"
	overlayPromoteChunkBranchDeltaMax = 2048
)

// IsOverlayPromoteConflictProtected reports tool output that must not be LLM-summarized
// (preview_overlay / promote_overlay assessments with conflicts or spill_path).
func IsOverlayPromoteConflictProtected(content string) bool {
	return inspectOverlayPromoteToolContent(content).protected
}

// TrimOverlayPromoteChunk shrinks oversized overlay promote tool output without
// LLM summarization — keeps path_status, clean_paths, spill_path, and guidance banners.
func TrimOverlayPromoteChunk(content string) (string, bool) {
	inspection := inspectOverlayPromoteToolContent(content)
	if !inspection.protected || !inspection.validPayload {
		return "", false
	}
	minimal := overlayPromoteChunkMinimal(inspection.result)
	raw, err := surveyjson.Marshal(minimal)
	if err != nil {
		return "", false
	}
	out := overlayPromoteChunkPreservePrefix + string(raw)
	if inspection.banners != "" {
		out += inspection.banners
	}
	return out, true
}

func overlayPromoteChunkMinimal(result api.WorkerMergeResult) api.WorkerMergeResult {
	out := api.WorkerMergeResult{
		JobID:         result.JobID,
		Mode:          result.Mode,
		Status:        result.Status,
		Paths:         append([]string(nil), result.Paths...),
		CleanPaths:    append([]string(nil), result.CleanPaths...),
		Applied:       append([]string(nil), result.Applied...),
		PathStatus:    append([]api.WorkerPromotePathStatus(nil), result.PathStatus...),
		SpillPath:     strings.TrimSpace(result.SpillPath),
		PromoteOrder:  result.PromoteOrder,
		PromoteAfter:  append([]string(nil), result.PromoteAfter...),
		BlockedBy:     append([]string(nil), result.BlockedBy...),
		OverlapJobIDs: append([]string(nil), result.OverlapJobIDs...),
	}
	if result.OverlayIntent != nil {
		cp := *result.OverlayIntent
		out.OverlayIntent = &cp
	}
	for _, row := range result.ConflictDigest {
		out.ConflictDigest = append(out.ConflictDigest, trimOverlayConflictDigestRow(row))
	}
	if len(out.ConflictDigest) == 0 {
		for _, row := range result.PathStatus {
			if row.Status != api.WorkerPromotePathOutcomeConflict {
				continue
			}
			out.ConflictDigest = append(out.ConflictDigest, api.WorkerPromoteConflictDigest{
				Path:         row.Path,
				ConflictTier: row.ConflictTier,
			})
		}
	}
	return out
}

func trimOverlayConflictDigestRow(row api.WorkerPromoteConflictDigest) api.WorkerPromoteConflictDigest {
	return api.WorkerPromoteConflictDigest{
		Path:         row.Path,
		ConflictTier: row.ConflictTier,
		BranchDelta:  capOverlayPromoteBranchDelta(row.BranchDelta),
	}
}

func capOverlayPromoteBranchDelta(delta string) string {
	delta = strings.TrimSpace(delta)
	if delta == "" {
		return ""
	}
	if len(delta) <= overlayPromoteChunkBranchDeltaMax {
		return delta
	}
	return delta[:overlayPromoteChunkBranchDeltaMax] + "\n…"
}

func overlayPromoteHasConflictPath(result api.WorkerMergeResult) bool {
	for _, row := range result.PathStatus {
		if row.Status == api.WorkerPromotePathOutcomeConflict {
			return true
		}
	}
	return false
}

type overlayPromoteInspection struct {
	result       api.WorkerMergeResult
	banners      string
	protected    bool
	validPayload bool
}

func inspectOverlayPromoteToolContent(content string) overlayPromoteInspection {
	content = strings.TrimSpace(content)
	if content == "" {
		return overlayPromoteInspection{}
	}
	inspection := overlayPromoteInspection{
		protected: strings.Contains(content, "OVERLAY_PROMOTE_SPILL") ||
			strings.Contains(content, "BANNER_PROMOTE_CONFLICT_DIGEST"),
	}
	_, jsonPart, banners, ok := hostmarker.SplitToolJSONBody(content)
	inspection.banners = banners
	inspection.validPayload = ok
	if !inspection.validPayload {
		return inspection
	}
	if err := json.Unmarshal([]byte(jsonPart), &inspection.result); err != nil {
		inspection.validPayload = false
		return inspection
	}
	inspection.protected = inspection.protected ||
		len(inspection.result.ConflictDigest) > 0 ||
		len(inspection.result.Conflicts) > 0 ||
		strings.TrimSpace(inspection.result.SpillPath) != "" ||
		overlayPromoteHasConflictPath(inspection.result)
	return inspection
}
