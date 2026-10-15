package guard

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/pkg/api"
)

const ProgressMissingCode = "PROGRESS_MISSING"

// ObserveProgressMissingBeforeDispatch publishes progress-missing facts.
func ObserveProgressMissingBeforeDispatch(
	sess *api.Session,
	progressContent, tool string,
	reviewLoopActive bool,
	gc *oar.GuardContext,
) {
	if gc == nil || sess == nil || sess.IsWorkerChild() {
		return
	}
	gc.Workflow.ReviewLoopActive = reviewLoopActive
	gc.Invocation.Tool = strings.TrimSpace(tool)
	gc.DeriveToolClassFacts()
	gc.Progress.ProgressGatedTool = progress.IsProgressGatedTool(tool)
	gc.Progress.ProgressMissing = progress.ProgressMissing(progressContent)
	if gc.Progress.ProgressGatedTool && gc.Progress.ProgressMissing && !reviewLoopActive {
		data := map[string]any{"tool": strings.TrimSpace(tool)}
		if call := rejectedProgressCall(gc.Invocation.Tool, gc.Invocation.ToolArgs); call != nil {
			data["rejected_call"] = call
		}
		gc.PutRejectData(ProgressMissingCode, data)
	}
}

// Preserve an exact argument excerpt as data, never a truncated replacement call.
func rejectedProgressCall(tool string, args map[string]any) map[string]any {
	path, ok := args["path"].(string)
	if !ok || path == "" || !utf8.ValidString(path) {
		return nil
	}
	encoded, _ := json.Marshal(struct {
		Tool            string            `json:"tool"`
		ArgumentExcerpt map[string]string `json:"argument_excerpt"`
	}{
		Tool:            tool,
		ArgumentExcerpt: map[string]string{"path": path},
	})
	if len(encoded) > 512 {
		return nil
	}
	return map[string]any{"tool": tool, "argument_excerpt": map[string]any{"path": path}}
}
