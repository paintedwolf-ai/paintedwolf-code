package guidance

import (
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/workercompletionxml"
	"github.com/lycaon/lycaon/pkg/api"
)

// LatestVisualArtifactIDsSinceUserIntent returns presentable visual store ids
// since the last user intent. Newest last: parent stills, then worker proof
// visual_artifact_ids. Live-tool video is excluded; the live slot already presents it.
func LatestVisualArtifactIDsSinceUserIntent(history []api.Message, max int) []string {
	if max <= 0 {
		max = 1
	}
	ids := PresentableVisualArtifactIDs(history)
	if len(ids) == 0 {
		return nil
	}
	if len(ids) > max {
		ids = ids[len(ids)-max:]
	}
	return ids
}

// PresentableVisualArtifactIDs returns every presentable still since the last
// user intent, oldest first — the set a turn may present.
func PresentableVisualArtifactIDs(history []api.Message) []string {
	start := api.UserIntentBoundary(history)
	if start < 0 {
		start = 0
	}
	var ids []string
	seen := map[string]struct{}{}
	appendID := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	for i := start; i < len(history); i++ {
		msg := history[i]
		if msg.Role != api.MessageRoleTool {
			continue
		}
		if msg.ToolResult != nil && msg.ToolResult.Visual != nil {
			v := msg.ToolResult.Visual
			if v.PresentableStill() {
				appendID(v.ID)
			}
		}
		for _, id := range visualArtifactIDsFromWorkerProof(msg.Content) {
			appendID(id)
		}
	}
	return ids
}

func visualArtifactIDsFromWorkerProof(content string) []string {
	doc, ok := workercompletionxml.Unmarshal(content)
	if !ok {
		return nil
	}
	raw := strings.TrimSpace(doc.ProofJSON)
	if raw == "" {
		return nil
	}
	var proof struct {
		VisualArtifactIDs []string `json:"visual_artifact_ids"`
	}
	if err := json.Unmarshal([]byte(raw), &proof); err != nil {
		return nil
	}
	return proof.VisualArtifactIDs
}

// BindCloseoutPresentArtifacts uses report.ArtifactIDs when set; otherwise the
// latest presentable visuals since the user intent.
func BindCloseoutPresentArtifacts(report CoordinatorCompletionReport, history []api.Message) CoordinatorCompletionReport {
	report.Normalize()
	if len(report.ArtifactIDs) > 0 {
		return report
	}
	if ids := LatestVisualArtifactIDsSinceUserIntent(history, 3); len(ids) > 0 {
		report.ArtifactIDs = ids
	}
	return report
}
