package promptloop

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestApplyToolResultSidecars_autoDesignatesCover(t *testing.T) {
	store := visual.NewMemoryStore()
	var gotProject, gotRoot, gotArt string
	designated := 0
	designate := func(_ context.Context, projectID, rootSessionID, artifactID string) error {
		designated++
		gotProject, gotRoot, gotArt = projectID, rootSessionID, artifactID
		return nil
	}
	tr := &api.ToolResult{Content: `{"mime":"image/png"}`, ToolCallID: "call-1"}
	content := applyToolResultSidecars(
		context.Background(),
		store,
		designate,
		"proj-1",
		"root-1",
		"root-1",
		"capture_page",
		`{"mime":"image/png"}`,
		tr,
		toolCaptures{visual: &tools.VisualCapture{
			Bytes:     visual.TestPNG1x1Bytes(),
			Mime:      "image/png",
			Source:    api.VisualArtifactSourceCapture,
			Caption:   "board",
			Perceive:  true,
			Projected: true,
		}},
	)
	if designated != 1 {
		t.Fatalf("designate calls = %d want 1", designated)
	}
	if gotProject != "proj-1" || gotRoot != "root-1" || gotArt == "" {
		t.Fatalf("designate args project=%q root=%q art=%q", gotProject, gotRoot, gotArt)
	}
	if tr.Visual == nil || tr.Visual.ID != gotArt {
		t.Fatalf("visual wire = %#v", tr.Visual)
	}
	if !strings.Contains(content, `"artifact_id":"`+gotArt+`"`) {
		t.Fatalf("model-visible content missing artifact_id: %s", content)
	}
	if !strings.HasPrefix(content, `{"artifact_id":"`+gotArt+`"`) {
		t.Fatalf("artifact_id must lead JSON for compaction survival: %s", content)
	}
}

func TestApplyToolResultSidecars_stampsArtifactIDForAskUser(t *testing.T) {
	store := visual.NewMemoryStore()
	tr := &api.ToolResult{Content: `{"mime":"image/png","caption":"Variant A"}`, ToolCallID: "call-stamp"}
	content := applyToolResultSidecars(
		context.Background(),
		store,
		nil,
		"",
		"root-stamp",
		"root-stamp",
		"render_view",
		`{"mime":"image/png","caption":"Variant A"}`,
		tr,
		toolCaptures{visual: &tools.VisualCapture{
			Bytes:     visual.TestPNG1x1Bytes(),
			Mime:      "image/png",
			Source:    api.VisualArtifactSourceRender,
			Caption:   "Variant A",
			Perceive:  true,
			Projected: true,
		}},
	)
	if tr.Visual == nil || tr.Visual.ID == "" {
		t.Fatal("expected store visual id")
	}
	if !strings.Contains(content, `"artifact_id":"`+tr.Visual.ID+`"`) {
		t.Fatalf("content = %s want artifact_id %s", content, tr.Visual.ID)
	}
}

func TestApplyToolResultSidecars_designateFailureIsBestEffort(t *testing.T) {
	store := visual.NewMemoryStore()
	designate := func(context.Context, string, string, string) error {
		return errors.New("cover bind failed")
	}
	tr := &api.ToolResult{Content: `{"mime":"image/png"}`, ToolCallID: "call-3"}
	_ = applyToolResultSidecars(
		context.Background(),
		store,
		designate,
		"proj-1",
		"root-1",
		"root-1",
		"capture_page",
		`{"mime":"image/png"}`,
		tr,
		toolCaptures{visual: &tools.VisualCapture{
			Bytes:     visual.TestPNG1x1Bytes(),
			Mime:      "image/png",
			Source:    api.VisualArtifactSourceCapture,
			Projected: true,
		}},
	)
	if tr.Outcome == api.ToolResultOutcomeRejected {
		t.Fatalf("cover failure must not reject tool: outcome=%q", tr.Outcome)
	}
	if tr.Visual == nil {
		t.Fatal("visual still attached")
	}
}

func TestApplyToolResultSidecars_measureAnnotateDoesNotDesignateCover(t *testing.T) {
	store := visual.NewMemoryStore()
	designated := 0
	designate := func(context.Context, string, string, string) error {
		designated++
		return nil
	}
	tr := &api.ToolResult{Content: `{"mime":"image/png"}`, ToolCallID: "call-m"}
	_ = applyToolResultSidecars(
		context.Background(),
		store,
		designate,
		"proj-1",
		"root-1",
		"root-1",
		"measure_page",
		`{"mime":"image/png"}`,
		tr,
		toolCaptures{visual: &tools.VisualCapture{
			Bytes:     visual.TestPNG1x1Bytes(),
			Mime:      "image/png",
			Source:    api.VisualArtifactSourceCapture,
			Projected: true,
		}},
	)
	if designated != 0 {
		t.Fatalf("measure_page annotate must not become cover: designate=%d", designated)
	}
	if tr.Visual == nil {
		t.Fatal("visual still attached")
	}
}
