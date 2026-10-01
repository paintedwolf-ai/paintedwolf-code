package promptloop

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOverlayPromotionSurvivesToolResultRoundTrip(t *testing.T) {
	capture := &tools.ToolInvocationOut{OverlayPromotion: &api.OverlayPromotion{
		Files: []api.FileEditSnapshot{{RootID: "root", Path: "new.go", After: "merged"}},
	}}
	result := &api.ToolResult{Content: "promoted", ToolCallID: "merge-call"}
	applyToolResultSidecars(context.Background(), nil, nil, "project", "session", "session",
		"promote_overlay", result.Content, result, toolCapturesFrom(capture))
	raw, err := json.Marshal(result)
	testutil.FailErr(t, "marshal promotion result", err)
	var restored api.ToolResult
	testutil.FailErr(t, "restore promotion result", json.Unmarshal(raw, &restored))
	if restored.OverlayPromotion == nil || restored.OverlayPromotion.Files[0].After != "merged" {
		t.Fatalf("promotion snapshots missing: %#v", restored.OverlayPromotion)
	}
}
